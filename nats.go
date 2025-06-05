package main

import (
	"bytes"
	"github.com/apache/arrow/go/arrow"
	"github.com/apache/arrow/go/arrow/array"
	"github.com/apache/arrow/go/arrow/ipc"
	"github.com/apache/arrow/go/arrow/memory"
	"github.com/nats-io/nats.go"
	"os"
	"time"
)

func buildArrow(particles []float32) []byte {
	pool := memory.NewGoAllocator()
	schema := arrow.NewSchema(
		[]arrow.Field{
			{Name: "time", Type: arrow.PrimitiveTypes.Int64},
			{Name: "posX", Type: arrow.PrimitiveTypes.Float32},
			{Name: "posY", Type: arrow.PrimitiveTypes.Float32},
			{Name: "velX", Type: arrow.PrimitiveTypes.Float32},
			{Name: "velY", Type: arrow.PrimitiveTypes.Float32},
		},
		nil,
	)
	b := array.NewRecordBuilder(pool, schema)
	defer b.Release()

	now := time.Now().UnixMicro()
	for i := 0; i < NumParticles; i++ {
		pos := i * 4
		b.Field(0).(*array.Int64Builder).Append(now)
		b.Field(1).(*array.Float32Builder).Append(particles[pos])
		b.Field(2).(*array.Float32Builder).Append(particles[pos+1])
		b.Field(3).(*array.Float32Builder).Append(particles[pos+2])
		b.Field(4).(*array.Float32Builder).Append(particles[pos+3])
	}
	rec := b.NewRecord()
	defer rec.Release()

	buf := bytes.NewBuffer(nil)
	wr := ipc.NewWriter(buf, ipc.WithSchema(schema))
	err := wr.Write(rec)
	if err != nil {
		panic(err)
	}
	err = wr.Close()
	if err != nil {
		panic(err)
	}
	if len(buf.Bytes()) == 0 {
		panic("buffer is empty")
	}
	return buf.Bytes()
}

func Connect(particles chan []float32) {

	url := os.Getenv("NATS_URL")
	if url == "" {
		url = nats.DefaultURL
	}

	password := os.Getenv("NATS_PASSWORD")

	nc, err := nats.Connect(url, nats.UserInfo("sys", password))
	if err != nil {
		panic(err)
	}
	defer nc.Drain()
	for data := range particles {
		if data == nil || len(data) < 4 {
			continue
		}
		msg := buildArrow(data)
		err := nc.Publish("sensors.flock", msg)
		if err != nil {
			panic(err)
		}
	}
}
