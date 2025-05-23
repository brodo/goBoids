package main

import (
	"fmt"
	"github.com/hamba/avro/v2"
	"github.com/nats-io/nats.go"
	"os"
	"time"
)

type Row struct {
	Time  int64   `avro:"time"`
	Value float32 `avro:"value"`
}

//go:generate go tool stringer -type=SensorType
type SensorType int

const (
	Pos SensorType = iota
	Vel
)

//go:generate go tool stringer -type=Axis
type Axis int

const (
	X Axis = iota
	Y
	Z
)

func subject(id int, sType SensorType, axis Axis) string {
	return fmt.Sprintf("sensors.swarm.%d.%s.%s", id, sType, axis)
}

type SubjectRow struct {
	subject string
	row     []byte
}

const schemaStr = `{
    "type": "record",
    "name": "SensorRecord",
	"namespace": "org.hamba.avro",
    "fields": [
		{"name": "time", "type": "long", "logicalType": "timestamp-micros" },
        {"name": "value", "type": "float"}
	]
}`

func Connect(particles chan []float32) {
	schema, err := avro.Parse(schemaStr)
	if err != nil {
		panic(err)
	}

	url := os.Getenv("NATS_URL")
	if url == "" {
		url = nats.DefaultURL
	}

	nc, _ := nats.Connect(url)
	defer nc.Drain()
	row := Row{Time: time.Now().UnixMicro()}
	subjectRows := make([]SubjectRow, NumParticles*4)
	for data := range particles {
		if data == nil || len(data) < 4 {
			continue
		}
		for i := 0; i < NumParticles; i++ {
			pos := i * 4
			var avroData []byte
			row.Value = data[pos]
			avroData, err = avro.Marshal(schema, row)
			if err != nil {
				fmt.Println("Error marshaling data:", err)
				return
			}
			subjectRows[pos] = SubjectRow{
				subject: subject(i, Pos, X),
				row:     avroData,
			}

			row.Value = data[pos+1]
			avroData, err = avro.Marshal(schema, row)
			if err != nil {
				fmt.Println("Error marshaling data:", err)
				return
			}
			subjectRows[pos+1] = SubjectRow{
				subject: subject(i, Pos, Y),
				row:     avroData,
			}
			row.Value = data[pos+2]
			avroData, err = avro.Marshal(schema, row)
			if err != nil {
				fmt.Println("Error marshaling data:", err)
				return
			}
			subjectRows[pos+2] = SubjectRow{
				subject: subject(i, Vel, X),
				row:     avroData,
			}
			row.Value = data[pos+3]
			avroData, err = avro.Marshal(schema, row)
			if err != nil {
				fmt.Println("Error marshaling data:", err)
				return
			}
			subjectRows[pos+3] = SubjectRow{
				subject: subject(i, Vel, Y),
				row:     avroData,
			}
		}
		go func() {
			for _, sr := range subjectRows {
				err = nc.Publish(sr.subject, sr.row)
				if err != nil {
					fmt.Println("Error publishing data:", err)
				}
			}
		}()

	}

}
