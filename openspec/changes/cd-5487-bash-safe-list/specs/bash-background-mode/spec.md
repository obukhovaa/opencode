## REMOVED Requirements

### Requirement: The permission-exempt safe list covers only simple commands
**Reason**: CD-5487 removes all safe-list execution exemptions; wrappers on that
list otherwise bypass deny-all policies. Compound-command syntax continues to
be checked by CD-5483's common gate.

**Migration**: Apply configured bash rules to every invocation. Retain the
simple-command predicate only for parallel scheduling; it grants no authority.
