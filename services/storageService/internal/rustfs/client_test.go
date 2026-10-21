package rustfs

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func TestIsObjectNotFoundUnwrapsSDKErrors(t *testing.T) {
	responseErr := func(status int, cause error) error {
		return &smithy.OperationError{ServiceID: "S3", OperationName: "HeadObject", Err: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
			Err:      cause,
		}}
	}
	notFound := []error{
		responseErr(http.StatusNotFound, &types.NotFound{}),
		responseErr(http.StatusNotFound, errors.New("NotFound")),
		fmt.Errorf("wrapped: %w", &types.NoSuchKey{}),
	}
	for _, err := range notFound {
		if !isObjectNotFound(err) {
			t.Fatalf("404 was not recognised: %v", err)
		}
	}
	for _, err := range []error{responseErr(http.StatusForbidden, errors.New("AccessDenied")), errors.New("NotFound")} {
		if isObjectNotFound(err) {
			t.Fatalf("non-404 error was treated as missing object: %v", err)
		}
	}
}
