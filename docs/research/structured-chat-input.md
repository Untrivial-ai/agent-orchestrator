# Structured Chat input

Structured input is a provider protocol capability. A model printing a question,
reporting that a tool was accepted, or naming an unavailable tool does not create
a pending request. AO renders requests received through the active Chat driver.

## Provider coverage

| Transport | Native input paths | Answer delivery |
| --- | --- | --- |
| Codex app-server | Blocking `item/tool/requestUserInput`; asynchronous agent messages with `delivery: async` and `questions`; MCP form and URL elicitation | Blocking RPC response, or durable normal message for asynchronous questions |
| Claude Code ACP | `AskUserQuestion` through the bundled adapter's form elicitation; MCP forms and URL elicitation | ACP elicitation response |
| Cursor ACP | `cursor/ask_question`; standard ACP elicitation | Native extension or ACP response |
| Grok ACP | `x.ai/ask_user_question` and `_x.ai/ask_user_question`; standard ACP elicitation | Native extension response with choices and annotations |
| Other registered ACP drivers | Standard form/URL elicitation when emitted by the installed provider | ACP elicitation response |
| Terminal sessions | Provider's terminal interaction | Terminal input; no structured Chat form |

The generic ACP transport supports elicitation, but support in the transport does
not establish that a particular CLI emits it. In particular, the current OpenCode
ACP transport does not expose OpenCode's native HTTP question API, and AO's Pi ACP
adapter does not expose Pi RPC extension dialogs. Those need native transport
adapters or upstream ACP bridges. Antigravity's current terminal binding likewise
does not supply a Chat question transport. These are explicit coverage gaps.

## Lifecycle and UX

Desktop questions dock above the composer. Choice labels and descriptions remain
visible, custom answers stay paired with their question, and multiple questions
can be navigated before submission. Mobile uses the request dock for simple
questions and the existing typed form for richer schemas and URL requests. Secret
string fields mask input on both surfaces.

Blocking questions retain their RPC identity. An invalid answer leaves the request
pending; approvals cannot consume a structured input. Closing the controller
invalidates RPC-bound questions, because no live recipient remains.

Asynchronous questions use a separate durable response mode. They do not mark the
agent as waiting for input and remain available after the asking turn completes or
the controller is replaced. Accepting an answer creates a context-bearing human
message with a stable id through ordinary intake; busy conversations queue it.
Skipping or cancelling closes the question without sending a message. Native
history replay retains question text without creating new pending requests.

The daemon validates field names, offered choices, types, integer values, required
properties, length limits, and numeric bounds before forwarding accepted forms.
