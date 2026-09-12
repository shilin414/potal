"""
Aily integration exceptions.

Error codes align with the official Aily custom-agent error table so the
dispatcher can map retry/circuit-breaker behavior.
"""


class AilyError(Exception):
    """Base Aily integration error."""

    error_code = 'aily_error'
    retryable = False

    def __init__(self, message: str = '', *, code: int | None = None,
                 http_status: int | None = None, raw: dict | None = None):
        super().__init__(message or self.error_code)
        self.message = message or self.error_code
        self.code = code
        self.http_status = http_status
        self.raw = raw or {}


class AilyAuthError(AilyError):
    error_code = 'aily_auth_error'


class AilyRateLimitError(AilyError):
    """429 / frequency limit — retryable with backoff."""
    error_code = 'aily_rate_limit'
    retryable = True


class AilyServerError(AilyError):
    """5xx / code 50001 — retryable."""
    error_code = 'aily_server_error'
    retryable = True


class AilyTimeoutError(AilyError):
    error_code = 'aily_timeout'
    retryable = True


class AilyClientError(AilyError):
    """4xx non-retryable request error."""
    error_code = 'aily_client_error'
    retryable = False


class AilyCapabilityError(AilyError):
    """Operation unsupported by the Aily adapter (e.g. cancel)."""
    error_code = 'aily_capability_error'
    retryable = False


OFFICIAL_ERROR_CODES = {
    # Per 自定义智能体概述 error table.
    10002: 'chat not found',
    10006: 'agent OpenAPI channel disabled',
    10007: 'no permission on agent or attachment owner mismatch',
    10008: 'tenant OpenAPI access disabled',
    10009: 'agent identity type disabled',
    10010: 'app identity not in callable scope',
    10011: 'user identity not in visible scope',
    50001: 'internal error',
}
