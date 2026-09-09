package model

// NativeCapture identifies the recorded input contract, not trusted gameplay.
// Producer is recorder-supplied text; neither it nor the capture/session IDs
// authenticates a game server. Original protobuf records live in the raw replay.
type NativeCapture struct {
	CaptureID          string   `json:"capture_id"`
	Producer           string   `json:"producer,omitempty"`
	GameType           string   `json:"game_type"`
	FormatVersion      uint32   `json:"format_version"`
	FormatMinor        uint32   `json:"format_minor"`
	FormatPatch        uint32   `json:"format_patch"`
	FrameEncoding      string   `json:"frame_encoding"`
	SchemaRevision     string   `json:"schema_revision"`
	ContainerIntegrity string   `json:"container_integrity"`
	Limitations        []string `json:"limitations"`
}

func (n *NativeCapture) Clone() *NativeCapture {
	if n == nil {
		return nil
	}
	out := *n
	out.Limitations = append([]string(nil), n.Limitations...)
	return &out
}
