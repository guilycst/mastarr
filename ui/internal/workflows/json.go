package workflows

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const maxJSONDepth = 128

// rejectDuplicateKeys validates the raw JSON tree before typed decoding. This
// keeps immutable plan identity and nested action evidence lossless.
func rejectDuplicateKeys(body []byte) error {
	if len(body) == 0 || !utf8.Valid(body) {
		return errors.New("response body is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := walkJSON(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("response has trailing data")
		}
		return errors.New("response has trailing data")
	}
	return validateEmbeddedActionUnions(body)
}

// validateEmbeddedActionUnions closes the strict-decoding gap created by the
// generated ActionInput union. Its generated UnmarshalJSON method stores raw
// bytes and decodes the selected member with encoding/json, which otherwise
// skips DisallowUnknownFields for the union and its nested file objects.
func validateEmbeddedActionUnions(body []byte) error {
	root, err := jsonObject(body)
	if err != nil {
		return err
	}
	if action, ok := root["action"]; ok {
		if err := validateActionUnion(action); err != nil {
			return err
		}
	}
	items, ok := root["items"]
	if !ok {
		return nil
	}
	var entries []json.RawMessage
	if err := decodeJSON(items, &entries); err != nil {
		return errors.New("response action plan list is malformed")
	}
	for _, entry := range entries {
		object, err := jsonObject(entry)
		if err != nil {
			return err
		}
		if action, ok := object["action"]; ok {
			if err := validateActionUnion(action); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeJSON(raw []byte, destination interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("response has trailing data")
	}
	return nil
}

func jsonObject(raw []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := decodeJSON(raw, &object); err != nil || object == nil {
		return nil, errors.New("response object is malformed")
	}
	return object, nil
}

func objectFields(object map[string]json.RawMessage, allowed, required []string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key := range object {
		if _, ok := allowedSet[key]; !ok {
			return errors.New("response contains unknown action field")
		}
	}
	for _, key := range required {
		if _, ok := object[key]; !ok {
			return errors.New("response action field is missing")
		}
	}
	return nil
}

func rawString(object map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := object[key]
	if !ok {
		return "", false
	}
	var value string
	if decodeJSON(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func validateActionUnion(raw json.RawMessage) error {
	object, err := jsonObject(raw)
	if err != nil {
		return err
	}
	kind, ok := rawString(object, "kind")
	if !ok {
		return errors.New("response action discriminator is missing")
	}
	switch kind {
	case "arr.registration":
		if err := objectFields(object, []string{"kind", "connectionId", "mediaKind", "providerId", "fields"}, []string{"kind", "connectionId", "mediaKind", "providerId", "fields"}); err != nil {
			return err
		}
		fields, err := jsonObject(object["fields"])
		if err != nil {
			return err
		}
		return objectFields(fields, []string{"monitored", "qualityProfileId", "rootFolder", "seasonFolder", "seasons", "seriesType"}, nil)
	case "arr.import":
		if err := objectFields(object, []string{"kind", "connectionId", "files", "previewRevision", "registeredExternalId", "transfer"}, []string{"kind", "connectionId", "files", "previewRevision", "registeredExternalId", "transfer"}); err != nil {
			return err
		}
		return validateRawFiles(object["files"], false)
	case "fs.copy", "fs.hardlink":
		if err := objectFields(object, []string{"kind", "files"}, []string{"kind", "files"}); err != nil {
			return err
		}
		return validateRawFiles(object["files"], true)
	case "fs.move", "fs.rename":
		if err := objectFields(object, []string{"kind", "executor", "files"}, []string{"kind", "executor", "files"}); err != nil {
			return err
		}
		return validateRawFiles(object["files"], true)
	case "client.stop":
		return objectFields(object, []string{"kind", "connectionId", "clientItemIds"}, []string{"kind", "connectionId", "clientItemIds"})
	case "client.remove":
		return objectFields(object, []string{"kind", "connectionId", "clientItemIds", "retainPayload"}, []string{"kind", "connectionId", "clientItemIds", "retainPayload"})
	case "fs.trash":
		if err := objectFields(object, []string{"kind", "files", "retentionDays", "stoppedClientIds"}, []string{"kind", "files", "retentionDays"}); err != nil {
			return err
		}
		return validateRawFiles(object["files"], false)
	case "fs.restore":
		if err := objectFields(object, []string{"kind", "files", "trashId"}, []string{"kind", "files", "trashId"}); err != nil {
			return err
		}
		return validateRawFiles(object["files"], true)
	case "fs.delete":
		if err := objectFields(object, []string{"kind", "files", "permanent", "irreversibleAcknowledgement"}, []string{"kind", "files", "permanent", "irreversibleAcknowledgement"}); err != nil {
			return err
		}
		return validateRawFiles(object["files"], false)
	case "descriptor.delete":
		return objectFields(object, []string{"kind", "descriptorIds", "irreversibleAcknowledgement"}, []string{"kind", "descriptorIds", "irreversibleAcknowledgement"})
	case "jellyfin.refresh":
		if err := objectFields(object, []string{"kind", "connectionId", "scope", "itemId"}, []string{"kind", "connectionId", "scope"}); err != nil {
			return err
		}
		scope, ok := rawString(object, "scope")
		if !ok || (scope != "library" && scope != "item") {
			return errors.New("response refresh scope is unknown")
		}
		if scope == "item" {
			if _, ok := object["itemId"]; !ok {
				return errors.New("response refresh item identity is missing")
			}
		} else if _, ok := object["itemId"]; ok {
			return errors.New("response library refresh has an item identity")
		}
		return nil
	default:
		return errors.New("response action discriminator is unknown")
	}
}

func validateRawFiles(raw json.RawMessage, mapped bool) error {
	var entries []json.RawMessage
	if err := decodeJSON(raw, &entries); err != nil {
		return errors.New("response action files are malformed")
	}
	for _, entry := range entries {
		object, err := jsonObject(entry)
		if err != nil {
			return err
		}
		if mapped {
			if err := objectFields(object, []string{"source", "destination"}, []string{"source", "destination"}); err != nil {
				return err
			}
			if err := validateRawTarget(object["source"]); err != nil {
				return err
			}
			if err := validateRawTarget(object["destination"]); err != nil {
				return err
			}
			continue
		}
		allowed := []string{"source", "movieOrEpisodeId", "subtitle", "language", "forced", "hearingImpaired"}
		required := []string{"source", "movieOrEpisodeId"}
		if err := objectFields(object, allowed, required); err != nil {
			return err
		}
		if err := validateRawTarget(object["source"]); err != nil {
			return err
		}
	}
	return nil
}

func validateRawTarget(raw json.RawMessage) error {
	object, err := jsonObject(raw)
	if err != nil {
		return err
	}
	return objectFields(object, []string{"rootId", "relativePath"}, []string{"rootId", "relativePath"})
}

func walkJSON(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return errors.New("response nesting is too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return errors.New("response JSON is malformed")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return errors.New("response object is malformed")
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("response object key is malformed")
			}
			if _, exists := seen[name]; exists {
				return errors.New("response contains duplicate object key")
			}
			seen[name] = struct{}{}
			if err := walkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		close, err := decoder.Token()
		if err != nil || close != json.Delim('}') {
			return errors.New("response object is malformed")
		}
	case '[':
		for decoder.More() {
			if err := walkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		close, err := decoder.Token()
		if err != nil || close != json.Delim(']') {
			return errors.New("response array is malformed")
		}
	default:
		return errors.New("response JSON delimiter is malformed")
	}
	return nil
}
