package window

import (
	"encoding/json"
	"fmt"
)

type webviewCommand struct {
	Navigate string `json:"navigate,omitempty"`
	Eval     string `json:"eval,omitempty"`
}

type webviewEvent struct {
	Ready bool             `json:"ready,omitempty"`
	Mark  *json.RawMessage `json:"mark,omitempty"`
}

func EncodeNavigateCommand(url string) ([]byte, error) {
	return encodeWebviewCommand(webviewCommand{Navigate: url})
}

func EncodeEvalCommand(js string) ([]byte, error) {
	return encodeWebviewCommand(webviewCommand{Eval: js})
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
	if m.Text == "" {
		return m, fmt.Errorf("mark has no text")
	}
	return m, nil
}

func DecodeWebviewMark(raw json.RawMessage) (Mark, error) {
	return DecodeDuckMark(string(raw))
}
