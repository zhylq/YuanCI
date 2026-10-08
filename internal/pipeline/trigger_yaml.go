package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// ExtractTriggers decodes only event policy so executable field/type errors do
// not change which events are eligible. Parse shares its document and policy
// ambiguity checks before compiling any executable configuration.
func ExtractTriggers(source []byte) ([]Trigger, error) {
	root, err := decodePipelineDocument(source)
	if err != nil {
		return nil, err
	}
	node := triggerSection(root)
	if node == nil {
		return nil, nil
	}
	encoded, err := yaml.Marshal(node)
	if err != nil {
		return nil, fmt.Errorf("encode trigger policy: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	decoder.KnownFields(true)
	var triggers []Trigger
	if err := decoder.Decode(&triggers); err != nil {
		return nil, fmt.Errorf("decode trigger policy: %w", err)
	}
	if err := ValidateTriggers(triggers); err != nil {
		return nil, err
	}
	return triggers, nil
}

func decodePipelineDocument(source []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode pipeline document: %w", err)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("decode trailing pipeline document: %w", err)
		}
		return nil, errors.New("pipeline must contain exactly one YAML document")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("pipeline must be a YAML mapping")
	}
	root := document.Content[0]
	if err := validatePolicyKeys(root); err != nil {
		return nil, err
	}
	if node := triggerSection(root); node != nil {
		if err := validatePolicyNode(node); err != nil {
			return nil, err
		}
	}
	return root, nil
}

func triggerSection(root *yaml.Node) *yaml.Node {
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "triggers" {
			return root.Content[i+1]
		}
	}
	return nil
}

func validatePolicyKeys(node *yaml.Node) error {
	keys := make(map[string]bool, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" || keys[key.Value] {
			return fmt.Errorf("ambiguous pipeline policy key at line %d; keys must be unique strings without merges", key.Line)
		}
		keys[key.Value] = true
	}
	return nil
}

func validatePolicyNode(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode {
		return fmt.Errorf("trigger policy aliases are not supported at line %d", node.Line)
	}
	if node.Kind == yaml.MappingNode {
		if err := validatePolicyKeys(node); err != nil {
			return err
		}
	}
	for _, child := range node.Content {
		if err := validatePolicyNode(child); err != nil {
			return err
		}
	}
	return nil
}
