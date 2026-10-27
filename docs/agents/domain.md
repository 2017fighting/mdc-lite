# Domain Docs

探索代码库时，工程技能该怎么消费本仓的领域文档。

## 探索之前先读这些

- 仓库根目录的 **`GLOSSARY.md`**，或
- 若根目录存在 **`GLOSSARY-MAP.md`**：它指向每个 context 一份 `GLOSSARY.md`，把与当前话题相关的那些都读一遍。
- **`docs/adr/`**：读与你即将动手的区域相关的 ADR。多 context 仓里还要看 `src/<context>/docs/adr/` 下的 context 级决策。

这些文件若不存在，**安静地继续**。不要指出它们缺席，也不要主动提议创建它们。`/domain-modeling` 技能（经 `/grill-with-docs` 与 `/improve-codebase-architecture` 到达）会在术语或决策真正被敲定时惰性创建它们。

## 文件结构

本仓是**单 context**：

```
/
├── GLOSSARY.md
├── docs/
│   ├── adr/
│   │   ├── 0001-....md
│   │   └── 0002-....md
│   ├── research/     ← 带引用的取证文档（wayfinder 票的产物）
│   ├── specs/        ← 规格正文
│   └── agents/       ← 本目录
└── ...
```

## 使用术语表的词汇

当你的产出要命名一个领域概念（issue 标题、重构提案、假设、测试名），用 `GLOSSARY.md` 里定义的词。不要漂到术语表明确避开的同义词上。

如果需要的概念还没进术语表，那是个信号：要么你在发明这个项目不用的语言（回头想想），要么存在真实缺口（记下来给 `/domain-modeling`）。

## ADR 冲突要显式指出

如果你的产出与某条既有 ADR 相矛盾，显式摆出来，不要静默覆盖：

> _与 ADR-0007（……）冲突，但值得重开，因为……_
