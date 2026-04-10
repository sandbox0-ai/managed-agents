package apicontractutil

import (
	"net/http"

	contract "github.com/sandbox0-ai/managed-agent/internal/apicontract/generated"
)

func ErrorResponse(code, message string, requestID *string) contract.BetaErrorResponse {
	response := contract.BetaErrorResponse{
		Type:      contract.Error,
		RequestId: requestID,
	}

	var union contract.BetaErrorResponse_Error
	switch code {
	case "invalid_request_error":
		_ = union.FromBetaInvalidRequestError(contract.BetaInvalidRequestError{
			Type:    contract.InvalidRequestError,
			Message: message,
		})
	case "authentication_error":
		_ = union.FromBetaAuthenticationError(contract.BetaAuthenticationError{
			Type:    contract.AuthenticationError,
			Message: message,
		})
	case "billing_error":
		_ = union.FromBetaBillingError(contract.BetaBillingError{
			Type:    contract.BetaBillingErrorTypeBillingError,
			Message: message,
		})
	case "permission_error":
		_ = union.FromBetaPermissionError(contract.BetaPermissionError{
			Type:    contract.PermissionError,
			Message: message,
		})
	case "not_found_error":
		_ = union.FromBetaNotFoundError(contract.BetaNotFoundError{
			Type:    contract.NotFoundError,
			Message: message,
		})
	case "rate_limit_error":
		_ = union.FromBetaRateLimitError(contract.BetaRateLimitError{
			Type:    contract.RateLimitError,
			Message: message,
		})
	case "gateway_timeout_error":
		_ = union.FromBetaGatewayTimeoutError(contract.BetaGatewayTimeoutError{
			Type:    contract.TimeoutError,
			Message: message,
		})
	case "overloaded_error":
		_ = union.FromBetaOverloadedError(contract.BetaOverloadedError{
			Type:    contract.OverloadedError,
			Message: message,
		})
	default:
		_ = union.FromBetaAPIError(contract.BetaAPIError{
			Type:    contract.ApiError,
			Message: message,
		})
	}
	response.Error = union
	return response
}

func ErrorCodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusGatewayTimeout, http.StatusRequestTimeout:
		return "gateway_timeout_error"
	default:
		return "api_error"
	}
}
