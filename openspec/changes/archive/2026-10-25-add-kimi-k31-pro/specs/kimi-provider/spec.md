## ADDED Requirements

### Requirement: Kimi K3.1 Pro models are registered
The system SHALL register `kimi.kimi-k3.1-pro` (API `kimi-k3.1-pro`) and `kimi.kimi-k3.1-pro-highspeed` (API `kimi-k3.1-pro-highspeed`) on the native `kimi` provider. Each SHALL declare a 1,048,576-token context, 131,072 default output tokens, adaptive thinking with maximum effort support, and vision attachments. Standard pricing per million tokens SHALL be $2 input, $10 output, $2 cache writes (default 5-minute TTL), and $0.20 cache reads; Highspeed SHALL use $4 input, $20 output, $4 cache writes and the same $0.20 cache reads. Neither model SHALL advertise Anthropic task budgets or xhigh effort.

#### Scenario: Model selection and pricing
- **WHEN** an agent selects either new model
- **THEN** its native API model and capabilities are available in the registry and generated schema, and usage costs use the corresponding official prices

### Requirement: Kimi K3.1 Pro defaults to maximum effort
Native K3.1 Pro requests SHALL use the existing Anthropic-compatible endpoint with adaptive thinking and default effort `max`. Explicit `low`, `high`, and `max` effort SHALL be retained, and unsupported `xhigh` SHALL fall back to `high` consistently with existing K3 behavior.

#### Scenario: Unset effort
- **WHEN** a K3.1 Pro or Highspeed agent omits reasoningEffort
- **THEN** its resolved config and request carry maximum effort

#### Scenario: Explicit effort
- **WHEN** an agent configures low or high effort for either variant
- **THEN** its request uses the configured effort
