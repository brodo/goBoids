package main

import (
	_ "embed"
	"fmt"
	"github.com/cogentcore/webgpu/wgpu"
	"github.com/cogentcore/webgpu/wgpuglfw"
	"github.com/go-gl/glfw/v3.3/glfw"
	"math"
	"math/rand"
	"os"
	"runtime"
	"strings"
	"time"
)

var forceFallbackAdapter = os.Getenv("WGPU_FORCE_FALLBACK_ADAPTER") == "1"

func init() {
	runtime.LockOSThread()

	switch os.Getenv("WGPU_LOG_LEVEL") {
	case "OFF":
		wgpu.SetLogLevel(wgpu.LogLevelOff)
	case "ERROR":
		wgpu.SetLogLevel(wgpu.LogLevelError)
	case "WARN":
		wgpu.SetLogLevel(wgpu.LogLevelWarn)
	case "INFO":
		wgpu.SetLogLevel(wgpu.LogLevelInfo)
	case "DEBUG":
		wgpu.SetLogLevel(wgpu.LogLevelDebug)
	case "TRACE":
		wgpu.SetLogLevel(wgpu.LogLevelTrace)
	}
}

const (
	// number of boid particles to simulate
	NumParticles = 4096
	// number of single-particle calculations (invocations) in each gpu work group
	ParticlesPerGroup = 256 // if you update this, also update it in the shader.
	NumBuffers        = 15  // Number of staging buffers
)

//go:embed compute.wgsl
var compute string

//go:embed draw.wgsl
var draw string

type State struct {
	surface           *wgpu.Surface
	adapter           *wgpu.Adapter
	device            *wgpu.Device
	queue             *wgpu.Queue
	config            *wgpu.SurfaceConfiguration
	renderPipeline    *wgpu.RenderPipeline
	computePipeline   *wgpu.ComputePipeline
	vertexBuffer      *wgpu.Buffer
	particleBindGroup *wgpu.BindGroup
	particleBuffer    *wgpu.Buffer
	frameNum          uint64
	workGroupCount    uint32
	stagingBuffers    [NumBuffers]*wgpu.Buffer // For reading back data from GPU
	bufferMappedState [NumBuffers]bool         // Track which buffers are currently mapped
	nextReadbackIndex uint32                   // Next buffer to use for readback
	particleData      chan []float32           // Store the current particle data
}

func InitState(window *glfw.Window) (s *State, err error) {
	defer func() {
		if err != nil {
			fmt.Printf("Error initializing state: %v\n", err)
			s.Destroy()
			s = nil
		}
	}()
	s = &State{}
	s.particleData = make(chan []float32, NumBuffers)

	instance := wgpu.CreateInstance(nil)
	defer instance.Release()

	s.surface = instance.CreateSurface(wgpuglfw.GetSurfaceDescriptor(window))

	s.adapter, err = instance.RequestAdapter(&wgpu.RequestAdapterOptions{
		ForceFallbackAdapter: forceFallbackAdapter,
		CompatibleSurface:    s.surface,
	})
	if err != nil {
		return s, err
	}
	defer s.adapter.Release()

	s.device, err = s.adapter.RequestDevice(nil)
	if err != nil {
		return s, err
	}
	s.queue = s.device.GetQueue()

	caps := s.surface.GetCapabilities(s.adapter)

	width, height := window.GetSize()
	s.config = &wgpu.SurfaceConfiguration{
		Usage:       wgpu.TextureUsageRenderAttachment,
		Format:      caps.Formats[0],
		Width:       uint32(width),
		Height:      uint32(height),
		PresentMode: wgpu.PresentModeFifo,
		AlphaMode:   caps.AlphaModes[0],
	}

	s.surface.Configure(s.adapter, s.device, s.config)

	computeShader, err := s.device.CreateShaderModule(&wgpu.ShaderModuleDescriptor{
		Label: "compute.wgsl",
		WGSLDescriptor: &wgpu.ShaderModuleWGSLDescriptor{
			Code: compute,
		},
	})
	if err != nil {
		return s, err
	}
	defer computeShader.Release()

	drawShader, err := s.device.CreateShaderModule(&wgpu.ShaderModuleDescriptor{
		Label: "draw.wgsl",
		WGSLDescriptor: &wgpu.ShaderModuleWGSLDescriptor{
			Code: draw,
		},
	})
	if err != nil {
		return s, err
	}
	defer drawShader.Release()

	simParamData := []float32{
		1.0 / 60.0, // deltaTime - 60 fps
		0.1,        // maxForce
		0.5,        // maxSpeed
		0.8,        // alignmentWeight
		0.7,        // cohesionWeight
		0.9,        // separationWeight
		0.1,        // perceptionRadius
	}

	simParamBuffer, err := s.device.CreateBufferInit(&wgpu.BufferInitDescriptor{
		Label:    "Simulation Param Buffer",
		Contents: wgpu.ToBytes(simParamData[:]),
		Usage:    wgpu.BufferUsageUniform | wgpu.BufferUsageCopyDst,
	})
	if err != nil {
		return s, err
	}
	defer simParamBuffer.Release()

	s.renderPipeline, err = s.device.CreateRenderPipeline(&wgpu.RenderPipelineDescriptor{
		Vertex: wgpu.VertexState{
			Module:     drawShader,
			EntryPoint: "main_vs",
			Buffers: []wgpu.VertexBufferLayout{
				{
					ArrayStride: 4 * 4, // 4 f32s
					StepMode:    wgpu.VertexStepModeInstance,
					Attributes: []wgpu.VertexAttribute{
						{
							Format:         wgpu.VertexFormatFloat32x2,
							Offset:         0, // position
							ShaderLocation: 0,
						},
						{
							Format:         wgpu.VertexFormatFloat32x2,
							Offset:         0 + wgpu.VertexFormatFloat32x2.Size(), // velocity
							ShaderLocation: 1,
						},
					},
				},
				{
					ArrayStride: 2 * 4, // 2 f32s -> one vertex. This is filled by `vertexBufferData`
					StepMode:    wgpu.VertexStepModeVertex,
					Attributes: []wgpu.VertexAttribute{
						{
							Format:         wgpu.VertexFormatFloat32x2,
							Offset:         0,
							ShaderLocation: 2,
						},
					},
				},
			},
		},
		Fragment: &wgpu.FragmentState{
			Module:     drawShader,
			EntryPoint: "main_fs",
			Targets: []wgpu.ColorTargetState{
				{
					Format:    s.config.Format,
					Blend:     nil,
					WriteMask: wgpu.ColorWriteMaskAll,
				},
			},
		},
		Primitive: wgpu.PrimitiveState{
			Topology:  wgpu.PrimitiveTopologyTriangleList,
			FrontFace: wgpu.FrontFaceCCW,
		},
		Multisample: wgpu.MultisampleState{
			Count:                  1,
			Mask:                   0xFFFFFFFF,
			AlphaToCoverageEnabled: false,
		},
	})
	if err != nil {
		return s, err
	}

	s.computePipeline, err = s.device.CreateComputePipeline(&wgpu.ComputePipelineDescriptor{
		Label: "Compute pipeline",
		Compute: wgpu.ProgrammableStageDescriptor{
			Module:     computeShader,
			EntryPoint: "main",
		},
	})
	if err != nil {
		return s, err
	}
	// this defines the small triangle for each boid
	vertexBufferData := [...]float32{-0.0025, -0.005, 0.0025, -0.005, 0.001, 0.0025}
	s.vertexBuffer, err = s.device.CreateBufferInit(&wgpu.BufferInitDescriptor{
		Label:    "Vertex Buffer",
		Contents: wgpu.ToBytes(vertexBufferData[:]),
		Usage:    wgpu.BufferUsageVertex | wgpu.BufferUsageCopyDst,
	})
	if err != nil {
		return s, err
	}

	var initialParticleData [4 * NumParticles]float32
	rng := rand.NewSource(42)

	for i := 0; i < len(initialParticleData); i += 4 {
		initialParticleData[i+0] = float32(rng.Int63())/math.MaxInt64*2 - 1 // position x
		initialParticleData[i+1] = float32(rng.Int63())/math.MaxInt64*2 - 1 // position y

		// Random velocity direction with a consistent speed
		angle := float32(rng.Int63()) / math.MaxInt64 * 2 * math.Pi
		speed := float32(0.1)
		initialParticleData[i+2] = speed * float32(math.Cos(float64(angle))) // velocity x
		initialParticleData[i+3] = speed * float32(math.Sin(float64(angle))) // velocity y
	}

	particleBuffer, err := s.device.CreateBufferInit(&wgpu.BufferInitDescriptor{
		Label:    "Particle Buffer",
		Contents: wgpu.ToBytes(initialParticleData[:]),
		Usage: wgpu.BufferUsageVertex |
			wgpu.BufferUsageStorage |
			wgpu.BufferUsageCopySrc,
	})
	if err != nil {
		return s, err
	}

	s.particleBuffer = particleBuffer

	// Initialize staging buffers
	s.stagingBuffers = [NumBuffers]*wgpu.Buffer{}
	s.bufferMappedState = [NumBuffers]bool{} // All false by default

	for i := 0; i < NumBuffers; i++ {
		s.stagingBuffers[i], err = s.device.CreateBuffer(&wgpu.BufferDescriptor{
			Label:            fmt.Sprintf("Staging Buffer %d", i),
			Size:             uint64(4 * NumParticles * 4),
			Usage:            wgpu.BufferUsageMapRead | wgpu.BufferUsageCopyDst,
			MappedAtCreation: false,
		})
		if err != nil {
			return nil, err
		}
	}

	s.nextReadbackIndex = 0

	computeBindGroupLayout := s.computePipeline.GetBindGroupLayout(0)
	defer computeBindGroupLayout.Release()

	particleBindGroup, err := s.device.CreateBindGroup(&wgpu.BindGroupDescriptor{
		Layout: computeBindGroupLayout,
		Entries: []wgpu.BindGroupEntry{
			{
				Binding: 0,
				Buffer:  s.particleBuffer,
				Size:    wgpu.WholeSize,
			},
			{
				Binding: 1,
				Buffer:  simParamBuffer,
				Size:    wgpu.WholeSize,
			},
		},
	})
	if err != nil {
		return s, err
	}

	s.particleBindGroup = particleBindGroup

	s.workGroupCount = uint32(math.Ceil(float64(NumParticles) / float64(ParticlesPerGroup)))
	s.frameNum = uint64(0)

	return s, nil
}

func (s *State) Resize(width, height int) {
	if width > 0 && height > 0 {
		s.config.Width = uint32(width)
		s.config.Height = uint32(height)

		s.surface.Configure(s.adapter, s.device, s.config)
	}
}

func (s *State) Render() error {
	nextTexture, err := s.surface.GetCurrentTexture()
	if err != nil {
		return fmt.Errorf("failed to get current texture: %w", err)
	}
	view, err := nextTexture.CreateView(nil)
	if err != nil {
		return fmt.Errorf("failed to create view for texture: %w", err)

	}
	defer view.Release()

	commandEncoder, err := s.device.CreateCommandEncoder(nil)
	if err != nil {
		return fmt.Errorf("failed to create command encoder: %w", err)
	}
	defer commandEncoder.Release()

	computePass := commandEncoder.BeginComputePass(nil)
	computePass.SetPipeline(s.computePipeline)
	computePass.SetBindGroup(0, s.particleBindGroup, nil)
	computePass.DispatchWorkgroups(s.workGroupCount, 1, 1)
	err = computePass.End()
	if err != nil {
		return fmt.Errorf("failed to complete compute pass for texture: %w", err)
	}

	computePass.Release()

	// Find a currently unmapped buffer for this frame's readback
	var readbackBufferIndex uint32 = s.nextReadbackIndex
	for i := 0; i < NumBuffers; i++ {
		candidateIndex := (s.nextReadbackIndex + uint32(i)) % NumBuffers
		if !s.bufferMappedState[candidateIndex] {
			readbackBufferIndex = candidateIndex
			break
		}
	}

	// Only proceed with readback if we found an available buffer
	if !s.bufferMappedState[readbackBufferIndex] {
		// Now we can safely copy to this buffer
		err = commandEncoder.CopyBufferToBuffer(
			s.particleBuffer, // Source buffer (your particle buffer)
			0,
			s.stagingBuffers[readbackBufferIndex], // Destination buffer (one that's not mapped)
			0,
			uint64(4*NumParticles*4),
		)

		if err != nil {
			return fmt.Errorf("failed to copy buffer to buffer: %w", err)
		}

		// Update next readback index for next frame
		s.nextReadbackIndex = (readbackBufferIndex + 1) % NumBuffers
	}

	renderPass := commandEncoder.BeginRenderPass(&wgpu.RenderPassDescriptor{
		ColorAttachments: []wgpu.RenderPassColorAttachment{
			{
				View:    view,
				LoadOp:  wgpu.LoadOpLoad,
				StoreOp: wgpu.StoreOpStore,
			},
		},
	})
	renderPass.SetPipeline(s.renderPipeline)
	renderPass.SetVertexBuffer(0, s.particleBuffer, 0, wgpu.WholeSize)
	renderPass.SetVertexBuffer(1, s.vertexBuffer, 0, wgpu.WholeSize)
	renderPass.Draw(3, NumParticles, 0, 0)
	err = renderPass.End()
	if err != nil {
		return fmt.Errorf("failed to complete render pass for texture: %w", err)
	}
	renderPass.Release() // must release

	s.frameNum += 1

	cmdBuffer, err := commandEncoder.Finish(nil)
	if err != nil {
		return fmt.Errorf("failed to finish command buffer: %w", err)
	}
	defer cmdBuffer.Release()

	// Submit command buffer and present
	s.queue.Submit(cmdBuffer)
	s.surface.Present()

	if !s.bufferMappedState[readbackBufferIndex] {
		// Mark the buffer as mapped before starting the async operation
		s.bufferMappedState[readbackBufferIndex] = true

		err = s.stagingBuffers[readbackBufferIndex].MapAsync(wgpu.MapModeRead, 0, uint64(4*NumParticles*4),
			func(status wgpu.BufferMapAsyncStatus) {
				if status == wgpu.BufferMapAsyncStatusSuccess {
					// Read the data
					buffer := make([]byte, 4*NumParticles*4)
					copy(buffer, s.stagingBuffers[readbackBufferIndex].GetMappedRange(0, uint(4*NumParticles*4)))
					err = s.stagingBuffers[readbackBufferIndex].Unmap()
					floatData := wgpu.FromBytes[float32](buffer)
					// Copy to our CPU-side array
					select {
					case s.particleData <- floatData:
					default:
						fmt.Println("failed to send particle data to buffer")

					}
					if err != nil {
						fmt.Printf("failed to unmap staging buffer: %v\n", err)
					}
				}
				// Mark buffer as no longer mapped
				s.bufferMappedState[readbackBufferIndex] = false
			})

		if err != nil {
			fmt.Println("Error starting buffer readback:", err)
		}
	}

	return nil
}

func (s *State) Destroy() {
	// Release staging buffers
	for i := 0; i < NumBuffers; i++ {
		if s.stagingBuffers[i] != nil {
			s.stagingBuffers[i].Release()
			s.stagingBuffers[i] = nil
		}
	}
	if s.particleBindGroup != nil {
		s.particleBindGroup.Release()
	}
	if s.particleBuffer != nil {
		s.particleBuffer.Release()
	}
	if s.vertexBuffer != nil {
		s.vertexBuffer.Release()
		s.vertexBuffer = nil
	}
	if s.computePipeline != nil {
		s.computePipeline.Release()
		s.computePipeline = nil
	}
	if s.renderPipeline != nil {
		s.renderPipeline.Release()
		s.renderPipeline = nil
	}
	if s.config != nil {
		s.config = nil
	}
	if s.queue != nil {
		s.queue.Release()
		s.queue = nil
	}
	if s.device != nil {
		s.device.Release()
		s.device = nil
	}
	if s.surface != nil {
		s.surface.Release()
		s.surface = nil
	}
}

func main() {
	if err := glfw.Init(); err != nil {
		panic(err)
	}
	defer glfw.Terminate()

	glfw.WindowHint(glfw.ClientAPI, glfw.NoAPI)
	window, err := glfw.CreateWindow(1024, 768, "Boids", nil, nil)
	if err != nil {
		panic(err)
	}
	defer window.Destroy()

	s, err := InitState(window)
	if err != nil {
		panic(err)
	}
	defer s.Destroy()

	window.SetSizeCallback(func(w *glfw.Window, width, height int) {
		s.Resize(width, height)
	})

	go Connect(s.particleData)

	const targetFPS = 60
	const frameTime = time.Second / targetFPS

	nextFrame := time.Now()

	for !window.ShouldClose() {
		now := time.Now()
		// Only render if it's time for the next frame
		if now.After(nextFrame) || now.Equal(nextFrame) {

			glfw.PollEvents()
			err = s.Render()
			if err != nil {
				fmt.Println("an error occurred while rendering:", err)

				errstr := err.Error()
				switch {
				case strings.Contains(errstr, "Surface timed out"): // do nothing
				case strings.Contains(errstr, "Surface is outdated"): // do nothing
				case strings.Contains(errstr, "Surface was lost"): // do nothing
				default:
					panic(err)
				}
			}
			// Schedule next frame
			nextFrame = nextFrame.Add(frameTime)

			// Prevent falling too far behind
			if nextFrame.Before(now) {
				nextFrame = now.Add(frameTime)
			}

		} else {
			time.Sleep(time.Millisecond)
		}
	}
}
