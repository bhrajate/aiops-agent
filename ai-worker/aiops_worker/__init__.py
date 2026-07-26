"""AIOps AI reasoning plane worker package.

Implements a Temporal Python Worker that registers ``InvestigationWorkflow``
(the RCA investigation state machine) plus its Activities. All model calls and
internal-API calls live inside Activities; the Workflow itself stays
deterministic (see architecture doc sections 7.2 / 7.4).
"""

__version__ = "0.1.0"

# Frozen cross-language identifiers (see docs/INTEGRATION.md "Temporal 约定").
WORKFLOW_TYPE_NAME = "InvestigationWorkflow"
TASK_QUEUE = "investigation-ai"
