## MODIFIED Requirements

### Requirement: Editor height SHALL be computed in Update, not mutated in View

The editor component's effective textarea height MUST be a function of model state
computed when that state changes — specifically when the affordance row above the
textarea appears or disappears. It MUST NOT be computed or mutated inside `View()`.

The affordance row is a single line above the textarea shared by every affordance
the editor draws there: file attachments and the slash-invocation recognition hint.
When at least one affordance is present the textarea height SHALL be `m.height - 1`
to leave room for that one row; when none is present the height SHALL be `m.height`.
The reservation SHALL remain one row when several affordances are active at once.
The transition between the two states MUST happen in response to the messages
processed by `Update` that change an affordance — attachment add and remove, and a
change to the draft that alters which invocations are recognized.

Mutating `textarea.SetHeight` inside `View()` is prohibited because:
1. It violates the Elm/Bubble Tea model — `View` is a pure projection of state, not a
   place to produce side effects.
2. It leaves the textarea height incorrect when attachments are removed (the shrunken
   height persists until the next `SetSize` call).

#### Scenario: Attachment added shrinks textarea height

- **GIVEN** an editor component with `SetSize(80, 10)` applied and no attachments
- **WHEN** an attachment is added (the relevant message is processed by `Update`)
- **THEN** the textarea height becomes `m.height - 1` (9 in this example)
- **AND** subsequent calls to `View()` reflect this height without any additional mutation

#### Scenario: Attachment removed restores textarea height

- **GIVEN** an editor with one attachment and textarea height `m.height - 1`
- **WHEN** the last attachment is removed (the relevant message is processed by `Update`)
- **THEN** the textarea height reverts to `m.height`
- **AND** `View()` does not need to set height to render correctly

#### Scenario: A recognized invocation reserves the same single row

- **GIVEN** an editor component with `SetSize(80, 10)` applied, no attachments, and an
  empty draft
- **WHEN** the draft becomes a recognized slash invocation
- **THEN** the textarea height becomes `m.height - 1`
- **AND** adding an attachment while that invocation is still recognized leaves the
  height at `m.height - 1`, because both affordances share one row

#### Scenario: Editing the invocation away releases the row

- **GIVEN** an editor whose only affordance is a recognized slash invocation
- **WHEN** the draft is edited so that nothing is recognized
- **THEN** the textarea height reverts to `m.height` without a further `SetSize` call

#### Scenario: View is a pure projection

- **GIVEN** any editor state
- **WHEN** `View()` is called
- **THEN** no method on the textarea model that mutates state is invoked during the call
