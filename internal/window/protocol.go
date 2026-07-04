package window

import (
	"encoding/json"
	"fmt"
)

type webviewCommand struct {
	Navigate string `json:"navigate,omitempty"`
	Eval     string `json:"eval,omitempty"`
	Snapshot *Rect  `json:"snapshot,omitempty"`
	ID       string `json:"id,omitempty"`
}

type webviewEvent struct {
	Ready    bool             `json:"ready,omitempty"`
	Mark     *json.RawMessage `json:"mark,omitempty"`
	ID       string           `json:"id,omitempty"`
	Snapshot string           `json:"snapshot,omitempty"`
}

func EncodeNavigateCommand(url string) ([]byte, error) {
	return encodeWebviewCommand(webviewCommand{Navigate: url})
}

func EncodeEvalCommand(js string) ([]byte, error) {
	return encodeWebviewCommand(webviewCommand{Eval: js})
}

func EncodeSnapshotCommand(id string, rect Rect) ([]byte, error) {
	return encodeWebviewCommand(webviewCommand{ID: id, Snapshot: &rect})
}

func encodeWebviewCommand(cmd webviewCommand) ([]byte, error) {
	b, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func DecodeWebviewEvent(line []byte) (webviewEvent, error) {
	var ev webviewEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return ev, err
	}
	return ev, nil
}

func DecodeDuckMark(payload string) (Mark, error) {
	var m Mark
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		return m, err
	}
	m.defaultType()
	if m.Type == "highlight" && m.Text == "" {
		return m, fmt.Errorf("mark has no text")
	}
	if m.Type == "drawing" && len(m.Strokes) == 0 {
		return m, fmt.Errorf("drawing mark has no strokes")
	}
	return m, nil
}

func DecodeWebviewMark(raw json.RawMessage) (Mark, error) {
	return DecodeDuckMark(string(raw))
}
