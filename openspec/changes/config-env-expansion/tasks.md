## 1. Configuration loading and persistence

- [x] 1.1 Install safe single-pass JSON value interpolation on both Viper file loaders, preserving precedence and safe diagnostics.
- [x] 1.2 Preserve raw placeholders and unknown fields during typed config writeback.

## 2. Coverage and documentation

- [x] 2.1 Test expansion, escaping, types, precedence, missing variables and nested writeback through Viper.
- [x] 2.2 Document semantics and update the schema to accept referenced scalar values.
- [x] 2.3 Add and run a hermetic process e2e covering loading and persistence.

## 3. Verification and completion

- [ ] 3.1 Run make test, make test-e2e, schema/security/build checks and review changes.
- [ ] 3.2 Sync specifications and archive this change plus any proven-complete leftovers on the branch.
