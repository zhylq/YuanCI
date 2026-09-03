package runner_v1_test

import (
	"testing"

	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestResponsesNeverContainPrivateKeyMaterial(t *testing.T) {
	t.Parallel()

	for _, message := range []protoreflect.MessageDescriptor{
		(&runnerv1.RegisterResponse{}).ProtoReflect().Descriptor(),
		(&runnerv1.RotateCertificateResponse{}).ProtoReflect().Descriptor(),
	} {
		if field := message.Fields().ByName("private_key_pem"); field != nil {
			t.Fatalf("%s exposes forbidden field private_key_pem", message.FullName())
		}
	}
}

func TestAuthenticatedMessagesDoNotTrustBodyRunnerIdentity(t *testing.T) {
	t.Parallel()

	for _, message := range []protoreflect.MessageDescriptor{
		(&runnerv1.Heartbeat{}).ProtoReflect().Descriptor(),
		(&runnerv1.RotateCertificateRequest{}).ProtoReflect().Descriptor(),
	} {
		if field := message.Fields().ByName("runner_id"); field != nil {
			t.Fatalf("%s exposes body runner_id instead of using mTLS identity", message.FullName())
		}
	}
}

func TestRegistrationAndRotationRequireCSR(t *testing.T) {
	t.Parallel()

	for _, message := range []protoreflect.MessageDescriptor{
		(&runnerv1.RegisterRequest{}).ProtoReflect().Descriptor(),
		(&runnerv1.RotateCertificateRequest{}).ProtoReflect().Descriptor(),
	} {
		field := message.Fields().ByName("csr_pem")
		if field == nil || field.Kind() != protoreflect.BytesKind {
			t.Fatalf("%s must carry a bytes csr_pem field", message.FullName())
		}
	}
}

func TestSourceCredentialsAreSeparatedFromPlanAndMetadata(t *testing.T) {
	t.Parallel()

	assignment := (&runnerv1.JobAssignment{}).ProtoReflect().Descriptor()
	source := assignment.Fields().ByName("source")
	credential := assignment.Fields().ByName("credential")
	if source == nil || source.Message().FullName() != "yuanci.runner.v1.SourceCheckout" {
		t.Fatal("assignment does not expose a separate source descriptor")
	}
	if credential == nil || credential.Message().FullName() != "yuanci.runner.v1.EphemeralCredential" {
		t.Fatal("assignment does not expose a separate ephemeral credential")
	}
	if field := source.Message().Fields().ByName("token"); field != nil {
		t.Fatal("source metadata contains credential material")
	}
	token := credential.Message().Fields().ByName("token")
	if token == nil || token.Kind() != protoreflect.BytesKind {
		t.Fatal("ephemeral credential token must use a mutable bytes field")
	}
}
