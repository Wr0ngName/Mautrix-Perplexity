# Test Plan - mautrix-claude Bridge

This document outlines the comprehensive test suite created for the mautrix-claude bridge following Test-Driven Development (TDD) principles.

## Test Status: FAILING (Expected)

All tests are currently failing because the implementation has not been written yet. This is the correct TDD approach:
1. Write failing tests first
2. Implement features to make tests pass
3. Refactor while keeping tests green

## Test Files Created

### Claude API Client Tests (`pkg/claudeapi/`)

#### `client_test.go` (139 test cases)
Tests for the HTTP client and message API:

- **TestNewClient**: Validates client creation with correct defaults
  - Creates client with valid API key
  - Creates client with empty API key
  - Verifies BaseURL, Version, HTTPClient initialization

- **TestCreateMessage**: Tests message creation
  - Successful message creation
  - Handles empty messages (400 error)
  - Verifies correct headers (x-api-key, anthropic-version, content-type)

- **TestCreateMessageAPIErrors**: Tests API error handling
  - 401 Unauthorized (invalid API key)
  - 429 Rate Limit (too many requests)
  - 500 Internal Server Error

- **TestValidateAPIKey**: Tests API key validation
  - Valid API key (200 OK)
  - Invalid API key (401 Unauthorized)
  - Empty API key

- **TestCreateMessageWithContext**: Tests context cancellation
  - Cancels request when context is cancelled

**Coverage**: Authentication, message creation, error handling, context management

---

#### `streaming_test.go` (7+ test scenarios)
Tests for Server-Sent Events (SSE) streaming:

- **TestCreateMessageStream**: Tests streaming message creation
  - Receives all streaming events (message_start, content_block_start, content_block_delta, etc.)
  - Verifies event count and types
  - Tests SSE format parsing

- **TestParseSSELine**: Tests SSE line parsing
  - Parses message_start events
  - Parses content_block_delta events
  - Parses message_stop events
  - Ignores empty lines
  - Ignores comment lines

- **TestStreamHandlesErrors**: Tests error handling in streams
  - Handles connection errors (500 status)
  - Handles context cancellation
  - Verifies channel closure

- **TestStreamEventTypes**: Tests all event types
  - message_start
  - content_block_start
  - content_block_delta
  - content_block_stop
  - message_delta
  - message_stop
  - error

**Coverage**: SSE parsing, event streaming, error handling, context cancellation

---

#### `conversations_test.go` (15+ test scenarios)
Tests for conversation context management:

- **TestNewConversationManager**: Tests manager creation
  - Default max tokens
  - Custom max tokens
  - Zero max tokens

- **TestAddMessage**: Tests adding messages
  - User messages
  - Assistant messages
  - Empty messages

- **TestGetMessages**: Tests message retrieval
  - Returns messages in order
  - Returns empty slice for new manager

- **TestClear**: Tests clearing conversation history
  - Clears all messages
  - Can add messages after clear

- **TestMessageAlternation**: Tests user-assistant alternation
  - Enforces proper alternation
  - Handles consecutive same-role messages

- **TestConcurrentAccess**: Tests thread safety
  - Concurrent reads and writes
  - No race conditions

- **TestTrimToTokenLimit**: Tests token-based trimming
  - Does not trim when under limit
  - Trims when over limit

- **TestMessageContent**: Tests content structure
  - Supports text content
  - Proper content type

**Coverage**: Message management, thread safety, token limiting, content structure

---

#### `models_test.go` (40+ test cases)
Tests for Claude model definitions and validation:

- **TestValidateModel**: Tests model validation
  - Validates claude-opus-4.5
  - Validates claude-sonnet-4.5
  - Validates claude-3-5-sonnet
  - Validates claude-3-5-haiku
  - Validates claude-3-opus
  - Rejects invalid models (gpt-4, empty, unknown)
  - Case-sensitive validation

- **TestGetModelMaxTokens**: Tests token limits
  - Opus 4.5 limits (200k input, 16k output)
  - Sonnet 4.5 limits (200k input, 8k output)
  - Sonnet 3.5 limits (200k input, 8k output)
  - Haiku 3.5 limits (200k input, 8k output)
  - Opus 3 limits (200k input, 4k output)

- **TestDefaultModel**: Tests default model
  - Default model is not empty
  - Default model is valid

- **TestValidModels**: Tests model list
  - ValidModels contains at least one model
  - All models in list are valid
  - No duplicate models

- **TestModelConstants**: Tests model constant definitions
  - All constants defined
  - All constants are valid

- **TestGetModelInfo**: Tests model metadata
  - Gets info for valid models
  - Returns error for invalid models
  - Has name, max input/output tokens

- **TestGetModelFamily**: Tests model family detection
  - Opus family detection
  - Sonnet family detection
  - Haiku family detection

**Coverage**: Model validation, token limits, constants, metadata

---

#### `errors_test.go` (25+ test scenarios)
Tests for error handling and classification:

- **TestParseAPIError**: Tests API error parsing
  - Authentication errors (401)
  - Rate limit errors (429)
  - API errors (500)
  - Invalid request errors (400)
  - Malformed JSON
  - Empty body

- **TestIsRateLimitError**: Tests rate limit detection
  - Identifies rate_limit_error type
  - Identifies 429 status
  - Rejects non-rate-limit errors
  - Handles nil error

- **TestIsAuthError**: Tests authentication error detection
  - Identifies authentication_error type
  - Identifies permission_error type
  - Rejects non-auth errors
  - Handles nil error

- **TestGetRetryAfter**: Tests retry-after parsing
  - Parses retry-after from error
  - Returns zero for non-rate-limit errors
  - Returns zero for nil error

- **TestAPIErrorImplementsError**: Tests error interface
  - APIError implements error interface
  - Error() returns non-empty string
  - Error message contains error type

- **TestIsOverloadedError**: Tests overload detection
  - Identifies overloaded_error (529)
  - Rejects non-overloaded errors

- **TestIsInvalidRequestError**: Tests invalid request detection
  - Identifies invalid_request_error
  - Rejects other error types

**Coverage**: Error parsing, classification, retry logic, error interface

---

### Connector Tests (`pkg/connector/`)

#### `connector_test.go` (15+ test scenarios)
Tests for the bridge connector core:

- **TestNewConnector**: Tests connector creation
  - Creates new connector successfully

- **TestGetName**: Tests bridge metadata
  - Returns "Claude AI" display name
  - Returns "claude" network ID
  - Has non-empty NetworkURL
  - Has BeeperBridgeType
  - Has DefaultPort (different from candy bridge)

- **TestGetLoginFlows**: Tests login flow registration
  - Returns API key login flow
  - Does not have password flow
  - Does not have cookie flow

- **TestGetDBMetaTypes**: Tests metadata types
  - Returns Ghost metadata constructor
  - Returns Message metadata constructor
  - Returns Portal metadata constructor
  - Returns UserLogin metadata constructor
  - Constructors return correct types

- **TestGetCapabilities**: Tests network capabilities
  - Returns capabilities
  - Does not support disappearing messages

- **TestMetadataStructures**: Tests metadata fields
  - GhostMetadata has Model field
  - PortalMetadata has ConversationName, Model, SystemPrompt, Temperature
  - MessageMetadata has ClaudeMessageID, TokensUsed
  - UserLoginMetadata has APIKey, Email

- **TestImplementsInterfaces**: Tests interface compliance
  - Implements NetworkConnector interface

- **TestMakeClaudeGhostID**: Tests ghost ID generation
  - Creates ghost ID for different models
  - Ghost ID is not empty

- **TestMakeClaudePortalKey**: Tests portal key generation
  - Creates portal key from conversation ID
  - Portal key is not empty

- **TestMakeClaudeMessageID**: Tests message ID generation
  - Creates message ID from Claude message ID
  - Message ID is not empty

**Coverage**: Connector initialization, metadata, capabilities, ID generation

---

#### `login_test.go` (20+ test scenarios)
Tests for API key authentication:

- **TestAPIKeyLoginStart**: Tests login initiation
  - Returns user input step
  - Step type is LoginStepTypeUserInput
  - Step ID is "api_key"
  - Has instructions
  - Has API key input field
  - Field type is password

- **TestAPIKeyLoginSubmitUserInput**: Tests login submission
  - Accepts valid API key format (sk-ant-api03-*)
  - Rejects invalid prefix
  - Rejects empty API key
  - Rejects missing API key
  - Rejects wrong format

- **TestAPIKeyValidation**: Tests API key format validation
  - Valid sk-ant-api03 prefix
  - Valid sk-ant prefix
  - Invalid prefix
  - Empty key
  - Only prefix (too short)
  - Wrong case

- **TestCreateLogin**: Tests login creation
  - Creates API key login
  - Rejects unknown flow ID
  - Does not support password login
  - Does not support cookie login

- **TestAPIKeyStorage**: Tests credential storage
  - API key stored in metadata
  - Email field available in metadata

- **TestLoginSecurity**: Documentation test
  - API key should not be logged

**Coverage**: Login flows, API key validation, security

---

#### `config_test.go` (50+ test scenarios)
Tests for configuration validation:

- **TestConfigDefaults**: Tests default values
  - Has sensible defaults for all fields
  - Temperature in valid range (0-1)
  - MaxTokens is positive
  - RateLimitPerMinute is non-negative

- **TestConfigValidation**: Tests validation rules
  - Valid config passes
  - Invalid model rejected
  - Temperature too high/low rejected
  - MaxTokens too low/high rejected
  - Negative rate limit rejected

- **TestConfigModelValidation**: Tests model validation
  - Accepts all valid Claude models
  - Tests: opus-4.5, sonnet-4.5, sonnet-3.5, haiku-3.5, opus-3

- **TestConfigTemperatureRange**: Tests temperature bounds
  - Minimum (0.0)
  - Maximum (1.0)
  - Mid-range (0.7)
  - Below minimum (-0.1)
  - Above maximum (1.1)

- **TestConfigMaxTokensRange**: Tests token limits
  - Minimum (1)
  - Typical (4096)
  - High (16384)
  - Zero (invalid)
  - Negative (invalid)

- **TestExampleConfig**: Tests example configuration
  - Is valid YAML
  - Contains required fields
  - Has comments for guidance
  - Mentions Claude models

- **TestConfigSystemPrompt**: Tests system prompt
  - Allows custom system prompt
  - Allows empty system prompt

- **TestConfigConversationMaxAge**: Tests conversation age limits
  - Unlimited (0)
  - 24 hours
  - One week
  - Negative value (invalid)

- **TestConfigRateLimiting**: Tests rate limit configuration
  - Unlimited (0)
  - 60 per minute
  - 5 per minute
  - Negative value (invalid)

**Coverage**: Configuration validation, defaults, ranges, YAML structure

---

## Test Statistics

### Total Test Files: 8

### Total Test Scenarios: 200+

### Coverage Areas:

1. **API Client**: 139 test cases
   - HTTP client configuration
   - Message creation (streaming and non-streaming)
   - Error handling (401, 429, 500)
   - Context management
   - Header validation

2. **Streaming**: 7+ scenarios
   - SSE event parsing
   - Event type handling
   - Error and cancellation handling

3. **Conversations**: 15+ scenarios
   - Message management
   - Token limiting
   - Thread safety
   - Content structure

4. **Models**: 40+ test cases
   - Model validation
   - Token limits
   - Constants and metadata
   - Family detection

5. **Errors**: 25+ scenarios
   - Error parsing and classification
   - Retry logic
   - Error interface compliance

6. **Connector**: 15+ scenarios
   - Connector initialization
   - Metadata types
   - Capabilities
   - ID generation

7. **Login**: 20+ scenarios
   - API key validation
   - Login flows
   - Security

8. **Configuration**: 50+ scenarios
   - Validation rules
   - Default values
   - Range checking
   - YAML structure

---

## Running the Tests

### Run all tests:
```bash
go test ./pkg/... -v
```

### Run specific package:
```bash
go test ./pkg/claudeapi/... -v
go test ./pkg/connector/... -v
```

### Run with coverage:
```bash
go test ./pkg/... -cover -coverprofile=coverage.out
go tool cover -html=coverage.out
```

### Expected Result (Current State):
All tests should **FAIL** with compilation errors because the implementation doesn't exist yet.

Example output:
```
# go.mau.fi/mautrix-claude/pkg/claudeapi
pkg/claudeapi/client_test.go:32:14: undefined: NewClient
pkg/claudeapi/client_test.go:60:19: undefined: CreateMessageRequest
...
FAIL	go.mau.fi/mautrix-claude/pkg/claudeapi [build failed]
```

This is **correct and expected** in TDD!

---

## Next Steps for Implementation

### Phase 1: Claude API Client
1. Create `pkg/claudeapi/types.go` with all data structures
2. Create `pkg/claudeapi/client.go` with HTTP client
3. Create `pkg/claudeapi/streaming.go` with SSE support
4. Create `pkg/claudeapi/conversations.go` with context management
5. Create `pkg/claudeapi/models.go` with model definitions
6. Create `pkg/claudeapi/errors.go` with error handling

Run tests after each file - watch them turn green!

### Phase 2: Connector Layer
1. Update `pkg/connector/connector.go` (rename, update metadata)
2. Update `pkg/connector/config.go` (new Config struct)
3. Update `pkg/connector/login.go` (API key login)
4. Update `pkg/connector/client.go` (Claude integration)

Run tests after each update.

### Phase 3: Verification
Once all implementations are complete:
- All 200+ tests should **PASS**
- Coverage should be >80%
- No compilation errors

---

## Test Quality Features

### 1. Table-Driven Tests
Uses table-driven approach for comprehensive coverage:
```go
tests := []struct {
    name        string
    input       Type
    expectError bool
}{
    // Multiple test cases
}
```

### 2. Edge Cases
Tests cover:
- Empty inputs
- Nil values
- Boundary conditions
- Invalid formats
- Error conditions

### 3. Thread Safety
Includes concurrency tests:
- Concurrent reads/writes
- Race condition detection

### 4. Error Scenarios
Comprehensive error testing:
- API errors (401, 429, 500)
- Network errors
- Invalid inputs
- Context cancellation

### 5. Mock HTTP Servers
Uses `httptest.NewServer()` for isolated testing:
- No external dependencies
- Fast execution
- Deterministic results

---

## Success Criteria

Tests are considered complete when:

- [ ] All test files compile
- [ ] All tests pass (after implementation)
- [ ] Code coverage >80%
- [ ] All edge cases covered
- [ ] All error paths tested
- [ ] Thread safety verified
- [ ] Mock servers used (no real API calls)

---

## Document Information

- **Created**: 2026-01-24
- **Status**: Tests Written, Implementation Pending
- **Test Files**: 8
- **Test Scenarios**: 200+
- **Expected Outcome**: All tests currently FAILING (correct for TDD)

---

## Usage Notes for Developers

1. **Don't modify tests during implementation**
   - Tests define the contract
   - Implementation should satisfy tests
   - Only fix tests if requirements change

2. **Run tests frequently**
   - After each function implementation
   - Watch tests turn from red to green
   - Refactor with confidence

3. **Use test output for debugging**
   - Test failures show exactly what's wrong
   - Error messages are descriptive
   - Table-driven tests isolate failing cases

4. **Maintain test quality**
   - Keep tests fast (<1s per test)
   - Keep tests isolated (no dependencies)
   - Keep tests readable (clear names and structure)

---

**Ready for Implementation!**

The test suite provides a complete specification for the mautrix-claude bridge. 
Implementation can now begin with confidence that all requirements are well-defined and testable.
