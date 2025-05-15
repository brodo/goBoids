package main

import (
	"fmt"
	"github.com/hamba/avro/v2"
	"github.com/nats-io/nats.go"
	"os"
)

type Boid struct {
	PosX float32 `avro:"posX"`
	PosY float32 `avro:"posY"`
	VelX float32 `avro:"velX"`
	VelY float32 `avro:"velY"`
}

type Boids struct {
	Items []Boid `avro:"items"`
}

const schemaStr = `{
    "type": "record",
    "name": "Boids",
    "namespace": "org.hamba.avro",
    "fields": [
        {
            "name": "items",
            "type": {
                "type": "array",
                "items": {
                    "type": "record",
                    "name": "Boid",
                    "fields": [
                        {"name": "posX", "type": "float"},
                        {"name": "posY", "type": "float"},
                        {"name": "velX", "type": "float"},
                        {"name": "velY", "type": "float"}
                    ]
                }
            }
        }
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
	for data := range particles {
		boids := make([]Boid, NumParticles)
		for i := 0; i < NumParticles; i++ {
			pos := i * 4
			boids[i] = Boid{
				PosX: data[pos],
				PosY: data[pos+1],
				VelX: data[pos+2],
				VelY: data[pos+3],
			}
		}
		boidsWrapper := Boids{
			Items: boids,
		}

		avroData, err := avro.Marshal(schema, boidsWrapper)
		if err != nil {
			fmt.Println("Error marshaling data:", err)
			continue
		}

		// Publish the Avro-encoded data
		err = nc.Publish("boids", avroData)
		if err != nil {
			fmt.Println("Error publishing data:", err)
		}

	}

}
