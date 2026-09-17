package guestprotocolv1

import (
	"crypto/sha256"
	"encoding/json"
	"sort"
	"strconv"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// IntentDigest hashes a versioned semantic JSON projection of decoded fields,
// not serialized Protobuf bytes. Field-number keys are sorted by encoding/json;
// integers remain integers, and absent/default scalar values have one meaning.
func IntentDigest(c *Command) ([]byte, error) {
	if err := commandShape(c); err != nil {
		return nil, err
	}
	copy := proto.Clone(c).(*Command)
	copy.MessageId = ""
	copy.OperationId = ""
	copy.IssuedAt = nil
	copy.AdmissionExpiresAt = nil
	copy.IntentDigest = nil
	if inspect := copy.GetInspectState(); inspect != nil {
		sort.Slice(inspect.Sections, func(i, j int) bool { return inspect.Sections[i] < inspect.Sections[j] })
	}
	value := intentProjection(copy.ProtoReflect())
	b, err := json.Marshal(value)
	if err != nil {
		return nil, invalid("intent")
	}
	sum := sha256.Sum256(append([]byte("dbaas.guest.command.intent.v1\n"), b...))
	return sum[:], nil
}

func intentProjection(m protoreflect.Message) map[string]any {
	fields := map[string]any{}
	if r, ok := m.Interface().(*RepositoryIdentity); ok {
		r.Endpoint, _ = normalizedEndpoint(r.Endpoint)
		r.Prefix, _ = normalizedPrefix(r.Prefix)
	}
	_, encrypted := m.Interface().(*EncryptedCredential)
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if encrypted && (fd.Number() == 3 || fd.Number() == 4 || fd.Number() == 5) {
			return true
		}
		project := func(v protoreflect.Value) any {
			if fd.Kind() == protoreflect.MessageKind {
				return intentProjection(v.Message())
			}
			if fd.Kind() == protoreflect.EnumKind {
				return int32(v.Enum())
			}
			return v.Interface()
		}
		var value any
		if fd.IsList() {
			list := make([]any, v.List().Len())
			for i := range list {
				list[i] = project(v.List().Get(i))
			}
			value = list
		} else {
			value = project(v)
		}
		fields[strconv.Itoa(int(fd.Number()))] = value
		return true
	})
	return fields
}

// SealCommand sets the digest once the controller has frozen the full intent.
// Retrying publication must reuse both IDs and the same accepted intent.
func SealCommand(c *Command) error {
	digest, err := IntentDigest(c)
	if err != nil {
		return err
	}
	candidate := proto.Clone(c).(*Command)
	candidate.IntentDigest = digest
	if proto.Size(candidate) > MaxEncodedSize {
		return invalid("encoded_size")
	}
	c.IntentDigest = digest
	return ValidateCommand(c)
}
