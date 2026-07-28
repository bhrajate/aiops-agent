"""AIOps AI 推理面 worker 包。

实现一个 Temporal Python Worker,注册 ``InvestigationWorkflow``
(RCA 调查状态机)及其 Activity。所有模型调用与内部 API 调用都发生在 Activity 内,
工作流自身保持确定性(见架构文档 7.2 / 7.4 节)。
"""

__version__ = "0.1.0"

# 冻结的跨语言标识符(见 docs/INTEGRATION.md「Temporal 约定」)。
WORKFLOW_TYPE_NAME = "InvestigationWorkflow"
TASK_QUEUE = "investigation-ai"
