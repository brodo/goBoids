struct VertexOutput {
    @builtin(position) position: vec4<f32>,
    @location(0) color: vec4<f32>,
}

@vertex
fn main_vs(
    @location(0) particle_pos: vec2<f32>,
    @location(1) particle_vel: vec2<f32>,
    @location(2) position: vec2<f32>,
) -> VertexOutput{
    let angle = -atan2(particle_vel.x, particle_vel.y);
    let pos = vec2<f32>(
        position.x * cos(angle) - position.y * sin(angle),
        position.x * sin(angle) + position.y * cos(angle)
    );
    // Calculate color based on velocity
    let speed = length(particle_vel);
    let color = vec3<f32>(
        min(speed * 2, 1.0),       // Red increases with speed
        0.5,                          // Fixed green component
        max(1.0 - speed * 2.0, 0.0)   // Blue decreases with speed
    );

    var output: VertexOutput;
    output.position = vec4<f32>(pos + particle_pos, 0.0, 1.0);
    output.color = vec4<f32>(color, 1.0);
    return output;
}

@fragment
fn main_fs(@location(0) color: vec4<f32>) -> @location(0) vec4<f32> {
    return color;
}
