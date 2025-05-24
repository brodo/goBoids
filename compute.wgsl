struct Boid {
    position: vec2<f32>,
    velocity: vec2<f32>,
}

struct SimParams {
    deltaTime: f32,
    maxForce: f32,
    maxSpeed: f32,
    alignmentWeight: f32,
    cohesionWeight: f32,
    separationWeight: f32,
    perceptionRadius: f32,
}

@group(0) @binding(0) var<storage, read_write> boids: array<Boid>;
@group(0) @binding(1) var<uniform> params: SimParams;

fn limit_vector(v: vec2<f32>, max_length: f32) -> vec2<f32> {
    let length_sq = dot(v, v);
    if (length_sq > 0.0) {
        if (length_sq > max_length * max_length) {
            return normalize(v) * max_length;
        }
        return v;
    }
    return vec2<f32>(0.0);
}


@compute @workgroup_size(256)
fn main(@builtin(global_invocation_id) global_id: vec3<u32>) {
    let index = global_id.x;
    var current = boids[index];
    var alignment = vec2<f32>(0.0);
    var cohesion = vec2<f32>(0.0);
    var separation = vec2<f32>(0.0);
    var total_cohesion = 0;
    for (var i = 0u; i < arrayLength(&boids); i++) {
        if (i == index) {
            continue;
        }
        let other = boids[i];
        let d = distance(current.position, other.position);
        if (d < params.perceptionRadius) {
            total_cohesion++;
            alignment += other.velocity;
            cohesion += other.position;
            // Separation
            if (d < params.perceptionRadius * 0.5) {
                let diff = current.position - other.position;
                separation += normalize(diff) / d;
            }
        }
    }

    // Apply flocking behaviors
    alignment = limit_vector(normalize(alignment) * params.maxSpeed - current.velocity, params.maxForce);

    let center = cohesion / f32(total_cohesion);
    cohesion = limit_vector(normalize(center - current.position) * params.maxSpeed - current.velocity, params.maxForce);

    separation = limit_vector(normalize(separation) * params.maxSpeed - current.velocity, params.maxForce);

    // Update boid
    var acceleration = alignment * params.alignmentWeight +
                         cohesion * params.cohesionWeight + 
                         separation * params.separationWeight;

    current.velocity = limit_vector(current.velocity + acceleration, params.maxSpeed);
    current.position = current.position + current.velocity * params.deltaTime;
    current.position = clamp(current.position - 2 * floor((current.position + 1) /2 ), vec2(-1.0),vec2(1.0));

    boids[index] = current;
}

