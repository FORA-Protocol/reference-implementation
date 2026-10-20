package main

import (
	"encoding/json"
	"mime"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	// The FORA services and messages, registered for decode to find by the RPC
	// path a request is sent to.
	_ "github.com/FORA-Protocol/protocol/gen/go/fora/admin/v1"
	_ "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// The Connect protocol's unary content types, the only two a FORA party sends.
const (
	contentTypeProto = "application/proto"
	contentTypeJSON  = "application/json"
)

// render is the proto-JSON form a decoded message is recorded in: the FORA wire
// form, snake_case field names.
var render = protojson.MarshalOptions{UseProtoNames: true}

// decode reads the request message a body sent to a FORA RPC carries. path is
// the request path, /<package>.<Service>/<Method>. A body sent anywhere else is
// not a FORA message and is not decoded: messageType is empty and so is problem.
// A body sent to a FORA RPC that does not decode keeps messageType, with problem
// saying why; the body bytes are recorded either way.
func decode(path, contentType, contentEncoding string, body []byte) (messageType string, message json.RawMessage,
	problem string,
) {
	input := rpcInput(path)
	if input == nil {
		return "", nil, ""
	}
	messageType = string(input.FullName())
	if contentEncoding != "" && contentEncoding != "identity" {
		return messageType, nil, "the body is sent with Content-Encoding " + contentEncoding
	}
	kind, err := protoregistry.GlobalTypes.FindMessageByName(input.FullName())
	if err != nil {
		return messageType, nil, err.Error()
	}
	msg := kind.New().Interface()
	mediaType, _, _ := mime.ParseMediaType(contentType)
	switch mediaType {
	case contentTypeProto:
		err = proto.Unmarshal(body, msg)
	case contentTypeJSON:
		err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, msg)
	default:
		return messageType, nil, "the body is sent as " + contentType
	}
	if err != nil {
		return messageType, nil, err.Error()
	}
	out, err := render.Marshal(msg)
	if err != nil {
		return messageType, nil, err.Error()
	}
	return messageType, out, ""
}

// rpcInput is the request message of the FORA RPC at path, or nil when path
// names none.
func rpcInput(path string) protoreflect.MessageDescriptor {
	service, method, ok := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	if !ok || service == "" || method == "" || strings.Contains(method, "/") {
		return nil
	}
	found, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service))
	if err != nil {
		return nil
	}
	sd, isService := found.(protoreflect.ServiceDescriptor)
	if !isService {
		return nil
	}
	md := sd.Methods().ByName(protoreflect.Name(method))
	if md == nil {
		return nil
	}
	return md.Input()
}
