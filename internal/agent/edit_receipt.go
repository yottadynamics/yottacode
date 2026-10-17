package agent

import (
	"encoding/json"
	"fmt"

	"github.com/yottadynamics/yottacode/internal/edit/hashline"
)

// editReceipt is the compact byte-level contract emitted by anchored reads and
// accepted by text edit tools. Keeping the receipt together prevents the model
// from reconstructing a target from rendered, line-numbered output.
type editReceipt struct {
	Path            string `json:"path"`
	Offset          int    `json:"offset"`
	Length          int    `json:"length"`
	Hash            string `json:"hash"`
	Old             string `json:"old"`
	EndsWithNewline bool   `json:"ends_with_newline"`
	Complete        bool   `json:"complete"`
}

func parseEditReceipt(raw json.RawMessage) (editReceipt, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return editReceipt{}, nil
	}
	var receipt editReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return editReceipt{}, fmt.Errorf("invalid receipt: %w", err)
	}
	if receipt.Offset < 0 || receipt.Length <= 0 {
		return editReceipt{}, fmt.Errorf("receipt offset must be non-negative and length must be positive")
	}
	if receipt.Length != len(receipt.Old) {
		return editReceipt{}, fmt.Errorf("receipt length %d does not match old text byte length %d", receipt.Length, len(receipt.Old))
	}
	anchor, err := hashline.HashSpan([]byte(receipt.Old), 0, len(receipt.Old))
	if err != nil {
		return editReceipt{}, fmt.Errorf("validate receipt: %w", err)
	}
	if receipt.Hash == "" || receipt.Hash != anchor.Hash {
		return editReceipt{}, fmt.Errorf("receipt hash does not match receipt old text")
	}
	if !receipt.Complete {
		return editReceipt{}, fmt.Errorf("receipt covers a truncated read; request a complete target range")
	}
	return receipt, nil
}
