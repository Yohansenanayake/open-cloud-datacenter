package guestprotocolv1

import (
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Reject ambiguous singular fields/oneofs before Protobuf's last-value-wins
// decoding. This also bounds nesting and rejects unknown fields at any depth.
func checkWire(b []byte, md protoreflect.MessageDescriptor, depth int) error {
	if depth > 32 {
		return invalid("wire.depth")
	}
	seen := map[protowire.Number]bool{}
	oneofs := map[protoreflect.FullName]bool{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return invalid("wire.tag")
		}
		b = b[n:]
		fd := md.Fields().ByNumber(num)
		if fd == nil {
			return invalid("wire.unknown_field")
		}
		if !fd.IsList() && seen[num] {
			return invalid("wire.duplicate_field")
		}
		seen[num] = true
		if one := fd.ContainingOneof(); one != nil {
			if oneofs[one.FullName()] {
				return invalid("wire.duplicate_oneof")
			}
			oneofs[one.FullName()] = true
		}
		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			return invalid("wire.value")
		}
		if fd.Kind() == protoreflect.MessageKind {
			if typ != protowire.BytesType {
				return invalid("wire.message")
			}
			nested, k := protowire.ConsumeBytes(b)
			if k < 0 {
				return invalid("wire.message")
			}
			if err := checkWire(nested, fd.Message(), depth+1); err != nil {
				return err
			}
		}
		b = b[n:]
	}
	return nil
}

func decode(data []byte, m proto.Message) error {
	if len(data) == 0 || len(data) > encodedLimit(m) {
		return invalid("encoded_size")
	}
	if err := checkWire(data, m.ProtoReflect().Descriptor(), 0); err != nil {
		return err
	}
	if err := (proto.UnmarshalOptions{RecursionLimit: 32}).Unmarshal(data, m); err != nil {
		return invalid("wire.encoding")
	}
	if unknownFields(m.ProtoReflect()) {
		return invalid("unknown_fields")
	}
	return nil
}
func encode(m proto.Message) ([]byte, error) {
	if m == nil || proto.Size(m) > encodedLimit(m) {
		return nil, invalid("encoded_size")
	}
	b, err := proto.Marshal(m)
	if err != nil {
		return nil, invalid("wire.encoding")
	}
	return b, nil
}

func encodedLimit(m proto.Message) int {
	switch m.(type) {
	case *LocalRequest, *LocalResponse:
		return MaxLocalEncodedSize
	}
	return MaxEncodedSize
}

func EncodeCommand(c *Command) ([]byte, error) {
	if err := ValidateCommand(c); err != nil {
		return nil, err
	}
	return encode(c)
}
func DecodeCommand(data []byte) (*Command, error) {
	c := new(Command)
	if err := decode(data, c); err != nil {
		return nil, err
	}
	if err := ValidateCommand(c); err != nil {
		return nil, err
	}
	return c, nil
}
func EncodeEvent(e *Event) ([]byte, error) {
	if err := ValidateEvent(e); err != nil {
		return nil, err
	}
	return encode(e)
}
func DecodeEvent(data []byte) (*Event, error) {
	e := new(Event)
	if err := decode(data, e); err != nil {
		return nil, err
	}
	if err := ValidateEvent(e); err != nil {
		return nil, err
	}
	return e, nil
}
func ValidateTask(t *TaskPayload) error {
	if t == nil || t.SchemaVersion != TaskSchemaVersion || !validID(t.OperationId) || !validID(t.InstanceUid) || unknownFields(t.ProtoReflect()) {
		return invalid("task.identity_or_schema")
	}
	return nil
}
func ValidateSafetyTask(t *SafetyTaskPayload) error {
	if t == nil || t.SchemaVersion != TaskSchemaVersion || !validID(t.OperationId) || !validID(t.InstanceUid) || t.CleanupGeneration == 0 || unknownFields(t.ProtoReflect()) {
		return invalid("safety_task.identity_or_schema")
	}
	return nil
}
func EncodeTask(t *TaskPayload) ([]byte, error) {
	if err := ValidateTask(t); err != nil {
		return nil, err
	}
	return encode(t)
}
func DecodeTask(data []byte) (*TaskPayload, error) {
	t := new(TaskPayload)
	if err := decode(data, t); err != nil {
		return nil, err
	}
	if err := ValidateTask(t); err != nil {
		return nil, err
	}
	return t, nil
}
func EncodeSafetyTask(t *SafetyTaskPayload) ([]byte, error) {
	if err := ValidateSafetyTask(t); err != nil {
		return nil, err
	}
	return encode(t)
}
func DecodeSafetyTask(data []byte) (*SafetyTaskPayload, error) {
	t := new(SafetyTaskPayload)
	if err := decode(data, t); err != nil {
		return nil, err
	}
	if err := ValidateSafetyTask(t); err != nil {
		return nil, err
	}
	return t, nil
}
func EncodeLocalRequest(r *LocalRequest) ([]byte, error) {
	if err := ValidateLocalRequest(r); err != nil {
		return nil, err
	}
	return encode(r)
}
func DecodeLocalRequest(data []byte) (*LocalRequest, error) {
	r := new(LocalRequest)
	if err := decode(data, r); err != nil {
		return nil, err
	}
	if err := ValidateLocalRequest(r); err != nil {
		return nil, err
	}
	return r, nil
}
func EncodeLocalResponse(r *LocalResponse) ([]byte, error) {
	if err := ValidateLocalResponse(r); err != nil {
		return nil, err
	}
	return encode(r)
}
func DecodeLocalResponse(data []byte) (*LocalResponse, error) {
	r := new(LocalResponse)
	if err := decode(data, r); err != nil {
		return nil, err
	}
	if err := ValidateLocalResponse(r); err != nil {
		return nil, err
	}
	return r, nil
}
