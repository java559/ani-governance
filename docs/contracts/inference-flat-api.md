# Inference 管理接口状态

本期 Governance 不提供 Inference 管理接口。普通和 GPU 推理的创建、列表、详情、操作查询、更新、启停/重启及删除均已下线；原 `/api/v1/inference/...` 报文不再是当前可调用合同。

Inference Go API 模块依赖、生成导入、出站客户端与环境配置一并移除，后续重新对接时重新确认正式上游版本并发布合同。Model 目录、版本和导入，以及 ACC / Resource CPU/GPU 实例链保持各自当前范围。本次不改变推理数据面的职责。
