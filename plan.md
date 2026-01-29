# Feature: Image Upload Support for Sidecar Mode

## Overview

Add support for sending images to Claude when using sidecar mode (Pro/Max subscription). Currently, images work fine in direct API mode but are **silently dropped** in sidecar mode because the Agent SDK's `query()` function only accepts a string prompt parameter, not structured multimodal content.

## Problem Summary

### Current Architecture
1. **Direct API Mode (Works)**: 
   - Go bridge sends `claudeapi.Content[]` with text and image blocks
   - Images passed as base64 via Anthropic API Messages endpoint
   - Claude Vision processes images correctly

2. **Sidecar Mode (Broken)**:
   - `extractMessageText()` in `pkg/sidecar/message_client.go:356-368` only extracts text, **silently drops images**
   - `ChatRequest` struct only has `Message string` field (no image support)
   - Python sidecar calls `query(prompt=text_string, options=...)` which doesn't support images

### Root Cause
The Claude Agent SDK's `query()` function signature is:
```python
async def query(prompt: str, options: ClaudeAgentOptions) -> AsyncIterator[Message]
```

The `prompt` parameter is a **string**, not structured content. The Agent SDK likely doesn't expose multimodal input in its high-level API.

## Requirements

- [ ] **REQ-1**: Pass image data from Go bridge to Python sidecar
- [ ] **REQ-2**: Determine if Agent SDK supports images (investigation needed)
- [ ] **REQ-3**: If Agent SDK doesn't support images, provide clear error message to user
- [ ] **REQ-4**: If Agent SDK supports images via undocumented method, implement it
- [ ] **REQ-5**: Maintain backward compatibility with text-only messages
- [ ] **REQ-6**: Add comprehensive tests for image handling

## Investigation Phase - COMPLETED

### Step 1: Research Agent SDK Image Support
**Status**: ✅ COMPLETED
**Finding**: **Agent SDK DOES support images via Streaming Input Mode**

According to the official documentation at https://platform.claude.com/docs/en/agent-sdk/streaming-vs-single-mode:

The Agent SDK supports images through **streaming input mode** using `ClaudeSDKClient`:

```python
async def message_generator():
    yield {
        "type": "user",
        "message": {
            "role": "user",
            "content": [
                {
                    "type": "text",
                    "text": "Review this architecture diagram"
                },
                {
                    "type": "image",
                    "source": {
                        "type": "base64",
                        "media_type": "image/png",
                        "data": image_data  # base64 encoded
                    }
                }
            ]
        }
    }

async with ClaudeSDKClient(options) as client:
    await client.query(message_generator())
```

**IMPORTANT LIMITATIONS**:
- Images are **NOT supported** with the simple `query()` function (single message input)
- Images **ARE supported** with `ClaudeSDKClient` and async generator input
- The sidecar must use streaming input mode to support images

**Decision**: Proceed with **Implementation Option A** - Full image support via streaming input

## Implementation Options

### Option A: Agent SDK Supports Images (Best Case)

If the Agent SDK provides image support (e.g., via `content` blocks or special fields):

#### Backend Changes (Go)

**File: `/mnt/data/git/mautrix-claude/pkg/sidecar/client.go`**
- [ ] Update `ChatRequest` struct to include image content:
  ```go
  type ChatRequest struct {
      PortalID        string              `json:"portal_id"`
      UserID          string              `json:"user_id,omitempty"`
      CredentialsJSON string              `json:"credentials_json,omitempty"`
      Message         string              `json:"message"`           // Keep for backward compat
      Content         []ContentBlock      `json:"content,omitempty"` // NEW: structured content
      SystemPrompt    *string             `json:"system_prompt,omitempty"`
      Model           *string             `json:"model,omitempty"`
      SessionID       string              `json:"session_id,omitempty"`
      Stream          bool                `json:"stream"`
  }
  
  type ContentBlock struct {
      Type      string       `json:"type"`              // "text" or "image"
      Text      string       `json:"text,omitempty"`    // For text blocks
      Source    *ImageSource `json:"source,omitempty"`  // For image blocks
  }
  
  type ImageSource struct {
      Type      string `json:"type"`       // "base64"
      MediaType string `json:"media_type"` // "image/jpeg", etc.
      Data      string `json:"data"`       // Base64 encoded
  }
  ```

**File: `/mnt/data/git/mautrix-claude/pkg/sidecar/message_client.go`**
- [ ] Update `extractMessageText()` to `extractMessageContent()`:
  ```go
  // extractMessageContent extracts structured content from messages
  func extractMessageContent(messages []claudeapi.Message) (string, []ContentBlock, error) {
      // Extract last user message
      for i := len(messages) - 1; i >= 0; i-- {
          if messages[i].Role == "user" {
              var textParts []string
              var contentBlocks []ContentBlock
              
              for _, content := range messages[i].Content {
                  switch content.Type {
                  case "text":
                      textParts = append(textParts, content.Text)
                      contentBlocks = append(contentBlocks, ContentBlock{
                          Type: "text",
                          Text: content.Text,
                      })
                  case "image":
                      if content.Source != nil {
                          contentBlocks = append(contentBlocks, ContentBlock{
                              Type: "image",
                              Source: &ImageSource{
                                  Type:      content.Source.Type,
                                  MediaType: content.Source.MediaType,
                                  Data:      content.Source.Data,
                              },
                          })
                      }
                  }
              }
              
              // Return both text (for backward compat) and structured content
              return strings.Join(textParts, "\n"), contentBlocks, nil
          }
      }
      return "", nil, fmt.Errorf("no user message found")
  }
  ```

- [ ] Update `CreateMessageStream()` to use `extractMessageContent()` and populate both `Message` (text) and `Content` (structured) fields in `ChatRequest`

#### Frontend Changes (Python)

**File: `/mnt/data/git/mautrix-claude/sidecar/main.py`**
- [ ] Update `ChatRequest` model to include content:
  ```python
  class ContentBlock(BaseModel):
      type: str  # "text" or "image"
      text: Optional[str] = None
      source: Optional[dict] = None  # {type, media_type, data}
  
  class ChatRequest(BaseModel):
      portal_id: str
      user_id: Optional[str] = None
      credentials_json: Optional[str] = None
      message: str  # Kept for backward compatibility
      content: Optional[list[ContentBlock]] = None  # NEW
      system_prompt: Optional[str] = None
      model: Optional[str] = None
      session_id: Optional[str] = None
      stream: bool = False
  ```

- [ ] Update `/v1/chat` endpoint to handle images:
  ```python
  @app.post("/v1/chat", response_model=ChatResponse)
  async def chat(request: ChatRequest):
      # ... existing code ...
      
      # Build prompt based on content type
      if request.content and any(block.type == "image" for block in request.content):
          # Has images - check if SDK supports it
          try:
              # Attempt to pass structured content to Agent SDK
              # (Implementation depends on SDK API discovered in investigation)
              query_result = await _run_query_with_multimodal_content(
                  content=request.content,
                  options=options,
                  timeout_seconds=QUERY_TIMEOUT,
                  portal_id=request.portal_id,
              )
          except NotImplementedError:
              # Agent SDK doesn't support images
              raise HTTPException(
                  status_code=400,
                  detail="Image uploads are not supported in sidecar mode with the current Agent SDK version. Use direct API mode for image support."
              )
      else:
          # Text-only - use existing code path
          query_result = await _run_query_with_timeout(...)
  ```

#### Error Handling
- [ ] If Agent SDK doesn't support images at runtime, return clear error
- [ ] Go bridge should catch this error and display to user
- [ ] Add `pkg/connector/client.go:formatUserFriendlyError()` case for image errors

### Option B: Agent SDK Does NOT Support Images (Graceful Degradation)

If Agent SDK has no image support:

#### Backend Changes (Go)

**File: `/mnt/data/git/mautrix-claude/pkg/connector/client.go`**
- [ ] Add check in `HandleMatrixMessage()` before sending to sidecar:
  ```go
  // Check if message has images and we're in sidecar mode
  if isSidecarMode && msg.Content.MsgType == event.MsgImage {
      errMsg := "Image uploads are not supported in sidecar mode (Pro/Max subscription). " +
               "To use Claude Vision with images, switch to API mode with an API key. " +
               "You can configure this in the bridge settings."
      c.sendErrorToRoom(ctx, msg.Portal, errMsg)
      return nil, errors.New(errMsg)
  }
  
  // Also check for images in text messages
  if isSidecarMode {
      hasImages := false
      for _, content := range messageContent {
          if content.Type == "image" {
              hasImages = true
              break
          }
      }
      if hasImages {
          errMsg := "Image uploads are not supported in sidecar mode..."
          c.sendErrorToRoom(ctx, msg.Portal, errMsg)
          return nil, errors.New(errMsg)
      }
  }
  ```

#### Documentation
- [ ] Update `/mnt/data/git/mautrix-claude/sidecar/README.md` to document limitation:
  ```markdown
  ## Limitations
  
  ### Image Support
  
  Image uploads are currently **not supported** in sidecar mode due to Agent SDK limitations. 
  If you need to use Claude Vision with images, use direct API mode instead:
  
  1. Obtain an Anthropic API key from https://console.anthropic.com
  2. Configure the bridge to use API mode instead of sidecar
  3. Images will work via the Messages API
  ```

### Option C: Hybrid Approach (Direct API Fallback for Images)

If we want the best of both worlds:

**Architecture**:
- Text-only messages: Use sidecar (Pro/Max subscription)
- Messages with images: Fall back to direct API (costs API credits)

**Pros**: Users can use images when needed, text-only uses subscription
**Cons**: Mixed costs, complexity, potential user confusion

#### Implementation
- [ ] Add `api_fallback_for_images` config option
- [ ] Check message type in `HandleMatrixMessage()`
- [ ] If images detected and `api_fallback_for_images=true`, use direct API client
- [ ] Send notice to user: "Note: This image message will use API credits instead of your subscription"

## Testing Strategy

### Unit Tests

**File: `/mnt/data/git/mautrix-claude/pkg/sidecar/message_client_test.go`**
- [ ] Test `extractMessageContent()` with:
  - Text-only message (existing behavior)
  - Image-only message
  - Image + text caption
  - Multiple images with text
  - Empty content
  - Mixed content blocks

**File: `/mnt/data/git/mautrix-claude/pkg/connector/client_test.go`**
- [ ] Test `HandleMatrixMessage()` with image in sidecar mode
- [ ] Verify error is sent to room
- [ ] Verify no silent failures

### Integration Tests

**File: `/mnt/data/git/mautrix-claude/pkg/sidecar/integration_test.go`**
- [ ] Test end-to-end image flow (if supported)
- [ ] Test error response from Python sidecar for images
- [ ] Test that text messages still work

**File: `/mnt/data/git/mautrix-claude/sidecar/test_main.py`** (create new)
- [ ] Test ChatRequest validation with images
- [ ] Test `/v1/chat` endpoint with image content
- [ ] Test error handling when Agent SDK doesn't support images

### Manual Testing Checklist
- [ ] Send text message in sidecar mode (should work)
- [ ] Upload image in sidecar mode (should show clear error OR work if implemented)
- [ ] Upload image in API mode (should work)
- [ ] Send image with caption in sidecar mode
- [ ] Test with different image formats (JPEG, PNG, GIF, WebP)
- [ ] Test with large images (>5MB)

## Risk Assessment

### Breaking Changes
- **None** - All changes are additive or error improvements
- Existing text-only sidecar functionality remains unchanged
- API mode unchanged

### Performance Considerations
- **Image size**: Base64 encoding adds ~33% overhead
  - Mitigation: Enforce 5MB limit (existing in API mode)
- **Token usage**: Images consume significant tokens
  - Mitigation: Already tracked in API mode, extend to sidecar

### Security Considerations
- **SECURITY-CRITICAL**: Validate image size before base64 encoding
- **SECURITY-CRITICAL**: Validate MIME types (already done in `isImageSupported()`)
- **SECURITY-CRITICAL**: Ensure per-user credentials are used (already implemented)

### Edge Cases
- [ ] User sends image while sidecar is down → fallback to API mode?
- [ ] Image download fails from Matrix → show error
- [ ] Image too large → show error with size limit
- [ ] Unsupported image format → show error with supported formats
- [ ] Agent SDK timeout with large image → increase timeout or show error

## Dependencies

### External
- **Claude Agent SDK**: Need to verify image support
  - If no support, need to wait for SDK update OR accept limitation
- **Python sidecar**: Must be running and authenticated

### Internal
- **Existing**: `downloadAndEncodeImage()` in `connector/client.go` (already implemented)
- **Existing**: `supportedImageTypes` validation (already implemented)
- **Existing**: Sidecar authentication and session management (already implemented)

## Estimated Complexity

### Backend (Go)
- **Low** if Agent SDK doesn't support (just add error handling)
- **Medium** if Agent SDK supports (update data structures, content extraction)

### Frontend (Python)
- **Low** if Agent SDK doesn't support (just validate and return error)
- **High** if Agent SDK supports but requires workarounds (custom API calls)

### Testing
- **Medium**: Need comprehensive coverage of image handling

### Overall
- **Best Case** (Option B - graceful error): **Low** (2-4 hours)
- **Typical Case** (Option A - SDK supports): **Medium** (1-2 days)
- **Worst Case** (Option C - hybrid fallback): **High** (3-5 days)

## Implementation Order

### Phase 1: Investigation (REQUIRED FIRST)
1. Research Agent SDK image support capabilities
2. Document findings in this plan
3. Choose implementation option (A, B, or C)

### Phase 2: Backend Changes (Go)
1. Update `ChatRequest` struct (if Option A)
2. Implement `extractMessageContent()` (if Option A)
3. Add error handling in `HandleMatrixMessage()` (all options)
4. Write unit tests

### Phase 3: Frontend Changes (Python)
1. Update `ChatRequest` model (if Option A)
2. Implement image handling in `/v1/chat` endpoint (if Option A)
3. Add validation and error messages (all options)
4. Write Python tests

### Phase 4: Integration & Testing
1. Run integration tests
2. Manual testing with real images
3. Performance testing with large images
4. Documentation updates

### Phase 5: Documentation & Rollout
1. Update README.md with image support status
2. Update user-facing docs
3. Add migration notes if needed
4. Deploy and monitor

## Next Steps

**IMMEDIATE ACTION REQUIRED**:
1. **Investigate Agent SDK** - Determine if multimodal support exists
2. Based on findings, update this plan with chosen option
3. Get approval for implementation approach
4. Proceed with implementation

**Recommended Approach**:
Start with **Option B** (graceful error) as it's:
- Quickest to implement (2-4 hours)
- No risk of breaking existing functionality
- Provides clear user experience
- Can be upgraded to Option A later if SDK adds support

Then monitor Agent SDK releases and upgrade to Option A when support is added.

## Open Questions

1. **Agent SDK Image Support**: Does it exist? (MUST ANSWER FIRST)
2. **User Preference**: Would users prefer Option C (hybrid) over Option B (clear error)?
3. **Fallback Strategy**: Should we auto-fallback to API mode or require explicit user action?
4. **Token Accounting**: How to estimate image token usage in sidecar mode?
5. **Multiple Images**: Does Agent SDK support multiple images per message?

## Success Criteria

- [ ] Users see clear error when uploading images in sidecar mode (if not supported)
- [ ] OR images work correctly in sidecar mode (if supported)
- [ ] No silent failures or dropped images
- [ ] Existing text-only functionality unchanged
- [ ] All tests passing
- [ ] Documentation updated
- [ ] Performance acceptable for typical images (< 5MB)
