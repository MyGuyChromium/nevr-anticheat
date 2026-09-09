package adapter

import "encoding/json"

// Array fields retain source compatibility for typed producers. A JSON decode
// always supplies explicit presence metadata: omitted/null/partial vectors are
// unavailable, while [0,0,0] is an observed stationary/origin measurement.
// Typed producers constructing these raw structs directly specify the arrays.
func vectorObserved(raw json.RawMessage) *bool {
	var components []*float64
	ok := json.Unmarshal(raw, &components) == nil && len(components) == 3
	if ok {
		for _, component := range components {
			if component == nil {
				ok = false
				break
			}
		}
	}
	return &ok
}

func (p *EchoVRPlayer) hasVelocity() bool {
	return p != nil && (p.velocityObserved == nil || *p.velocityObserved)
}
func (d *EchoVRDisc) hasPosition() bool {
	return d != nil && (d.positionObserved == nil || *d.positionObserved)
}
func (d *EchoVRDisc) hasVelocity() bool {
	return d != nil && (d.velocityObserved == nil || *d.velocityObserved)
}

type playerVectorJSON EchoVRPlayer

func (p *EchoVRPlayer) UnmarshalJSON(data []byte) error {
	var decoded playerVectorJSON
	integers := struct {
		*playerVectorJSON
		Level integralInt `json:"level"`
		Ping  integralInt `json:"ping"`
	}{playerVectorJSON: &decoded}
	if err := json.Unmarshal(data, &integers); err != nil {
		return err
	}
	decoded.Level, decoded.Ping = int(integers.Level), int(integers.Ping)
	var fields struct {
		Velocity json.RawMessage `json:"velocity"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	decoded.velocityObserved = vectorObserved(fields.Velocity)
	*p = EchoVRPlayer(decoded)
	return nil
}
func (p EchoVRPlayer) MarshalJSON() ([]byte, error) {
	var velocity *[3]float64
	if p.hasVelocity() {
		velocity = &p.Velocity
	}
	return json.Marshal(struct {
		*playerVectorJSON
		Velocity *[3]float64 `json:"velocity,omitempty"`
	}{(*playerVectorJSON)(&p), velocity})
}

type discVectorJSON EchoVRDisc

func (d *EchoVRDisc) UnmarshalJSON(data []byte) error {
	var decoded discVectorJSON
	integers := struct {
		*discVectorJSON
		BounceCount *integralInt `json:"bounce_count"`
	}{discVectorJSON: &decoded}
	if err := json.Unmarshal(data, &integers); err != nil {
		return err
	}
	if integers.BounceCount != nil {
		count := int(*integers.BounceCount)
		decoded.BounceCount = &count
	}
	var fields struct {
		Position json.RawMessage `json:"position"`
		Velocity json.RawMessage `json:"velocity"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	decoded.positionObserved, decoded.velocityObserved = vectorObserved(fields.Position), vectorObserved(fields.Velocity)
	*d = EchoVRDisc(decoded)
	return nil
}
func (d EchoVRDisc) MarshalJSON() ([]byte, error) {
	var position, velocity *[3]float64
	if d.hasPosition() {
		position = &d.Position
	}
	if d.hasVelocity() {
		velocity = &d.Velocity
	}
	return json.Marshal(struct {
		*discVectorJSON
		Position *[3]float64 `json:"position,omitempty"`
		Velocity *[3]float64 `json:"velocity,omitempty"`
	}{(*discVectorJSON)(&d), position, velocity})
}
