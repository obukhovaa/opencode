## 1. Bound reply
- [x] 1.1 Register and implement the authenticated exact-request/session reply operation.
- [x] 1.2 Clear router pending state and populate stale-click suppression on successful reply.
- [x] 1.3 Prevent delayed fan-out from restoring a settled question.
## 2. Validation and delivery
- [x] 2.1 Cover exact delivery, stale and cross-session refusal, duplicate suppression, nudge cessation and reply-before-fan-out locally.
- [x] 2.2 Run the required full repository checks and publish the follow-up PR for review.

## 3. Review corrections
- [x] 3.1 Preserve buffered input and prevent resurrection when a bound reply wins inside fan-out, including unbind cleanup.
- [x] 3.2 Add bounded digest-only acknowledgement receipts and single-call text parsing for lost-response retries.
- [x] 3.3 Validate prompt constraints, classify refusals, exclude custom text from click suppression, and preserve queued-input priority.
- [x] 3.4 Run focused/full checks and refresh the existing review request.

## 4. Final review follow-ups
- [x] 4.1 Requeue a buffered item lost to a bound reply at the head of the inbox, keeping later buffered messages in order.
- [x] 4.2 Match an exact option label before an index in the reply codec, and keep a whole-label Multiple reply unsplit.
- [x] 4.3 Restore the reply-before-fan-out scenario and fix spacing in the docs and spec.
