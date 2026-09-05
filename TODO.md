# TODO

## Effort alias exclusions

- [ ] Implement the [planned effort alias exclusion design](DESIGN.md#planned-effort-alias-exclusions).
  - Add `--exclude-efforts` / `GO_OPENAI_PROXY_EXCLUDE_EFFORTS` to server
    configuration and carry the list into resolver options. Default to no
    exclusions. Trim, lowercase, discard empty entries, and deduplicate; reject
    unknown efforts at startup with the invalid value and valid choices.
  - Pass the configuration through both startup and HTTP-handler resolver
    construction so startup output, model listing, and model lookup agree.
  - Filter catalog effort metadata before generating ordinary and Fast effort
    aliases. Preserve base models, supported plain Fast aliases, retained-entry
    ordering, deduplication, and existing model exclusions. Do not filter
    explicit `--models` entries by effort or change inference alias parsing.
  - Add table-driven tests for empty/default exclusions, normalization,
    duplicates, invalid values, individual and all effort exclusions, catalogs
    with and without Fast support, explicit model lists, and interaction with
    base-family and exact-alias model exclusions. Include a base slug ending in
    an effort-like suffix to verify filtering uses metadata.
  - Verify CLI/environment parsing and startup validation, consistent
    startup/list/lookup behavior, and unchanged inference for hidden aliases
    and explicit request efforts on both inference endpoints.
  - Update README configuration and discovery guidance when implemented, remove
    the planned designation in DESIGN.md, and mark this task complete.
  - Run `prek run --all-files` and `make check`.
