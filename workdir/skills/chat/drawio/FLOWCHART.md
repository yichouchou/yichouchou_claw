# TYPE 4: 流程图

## 需要向用户问清楚的信息

- 步骤和判断的清单
- 输入 / 输出和文档
- 分支条件

## Flowchart checklist

- ID 连续递增(无跳号)
- 每种类型用正确的 shape
- 判断节点带 Yes/No 输出
- 最小间距: 水平 180px / 垂直 140px
- 节点多时增大 `pageWidth`/`pageHeight`

## 最小 Flowchart 模板

```xml
<mxGraphModel page="1" pageWidth="850" pageHeight="1100">
  <root>
    <mxCell id="0"/>
    <mxCell id="1" parent="0"/>
    <mxCell id="10" value="开始" style="ellipse;" vertex="1" parent="1">
      <mxGeometry x="300" y="40" width="120" height="50" as="geometry"/>
    </mxCell>
    <mxCell id="11" value="处理" style="rounded=1;" vertex="1" parent="1">
      <mxGeometry x="300" y="140" width="150" height="50" as="geometry"/>
    </mxCell>
    <mxCell id="20" value="" style="endArrow=block;endFill=1;" edge="1" parent="1" source="10" target="11">
      <mxGeometry relative="1" as="geometry"/>
    </mxCell>
  </root>
</mxGraphModel>
```

## 布局规则

- 主流程走垂直方向
- 分支放两侧,留清晰间距
- 加大网格区分: `gridSize=20`
- 推荐间距: 列间 200px,行间 160px
- 超过 8 个节点,翻倍 page 宽或高

## 常见错误

- 判断画成处理节点
- 箭头缺 Yes/No 标签

## 何时反问用户

- 判断条件缺失
- 开始 / 结束节点不明确

## Shape 参考

| Shape | 样式关键字 | 用途 |
|---|---|---|
| 开始 / 结束 | `ellipse` | 终止节点 |
| 处理 / 动作 | `rounded=1` | 步骤和动作 |
| 判断 | `rhombus` | Yes/No 分支 |
| 文档 | `shape=document` | 报告或输出文档 |
| 数据 | `shape=parallelogram` | 输入或输出数据 |

## 配色

| Shape | fillColor | strokeColor |
|---|---|---|
| 开始 | `#d5e8d4` | `#82b366` |
| 处理 | `#dae8fc` | `#6c8ebf` |
| 判断 | `#fff2cc` | `#d6b656` |
| 结束 | `#f8cecc` | `#b85450` |

## 完整 XML 模板

```xml
<mxGraphModel dx="1422" dy="762" grid="1" gridSize="10" guides="1" tooltips="1" connect="1" arrows="1" fold="1" page="1" pageScale="1" pageWidth="850" pageHeight="1100" math="0" shadow="0">
  <root>
    <mxCell id="0"/>
    <mxCell id="1" parent="0"/>

    <mxCell id="10" value="开始"
      style="ellipse;whiteSpace=wrap;html=1;fillColor=#d5e8d4;strokeColor=#82b366;fontSize=13;fontStyle=1;"
      vertex="1" parent="1">
      <mxGeometry x="300" y="40" width="120" height="50" as="geometry"/>
    </mxCell>

    <mxCell id="11" value="接收请求"
      style="rounded=1;whiteSpace=wrap;html=1;fillColor=#dae8fc;strokeColor=#6c8ebf;fontSize=12;"
      vertex="1" parent="1">
      <mxGeometry x="300" y="140" width="150" height="50" as="geometry"/>
    </mxCell>

    <mxCell id="12" value="参数有效?"
      style="rhombus;whiteSpace=wrap;html=1;fillColor=#fff2cc;strokeColor=#d6b656;fontSize=12;"
      vertex="1" parent="1">
      <mxGeometry x="280" y="245" width="190" height="70" as="geometry"/>
    </mxCell>

    <mxCell id="13" value="处理数据"
      style="rounded=1;whiteSpace=wrap;html=1;fillColor=#dae8fc;strokeColor=#6c8ebf;fontSize=12;"
      vertex="1" parent="1">
      <mxGeometry x="300" y="370" width="150" height="50" as="geometry"/>
    </mxCell>

    <mxCell id="14" value="显示错误"
      style="rounded=1;whiteSpace=wrap;html=1;fillColor=#f8cecc;strokeColor=#b85450;fontSize=12;"
      vertex="1" parent="1">
      <mxGeometry x="530" y="245" width="130" height="50" as="geometry"/>
    </mxCell>

    <mxCell id="15" value="结束"
      style="ellipse;whiteSpace=wrap;html=1;fillColor=#f8cecc;strokeColor=#b85450;fontSize=13;fontStyle=1;"
      vertex="1" parent="1">
      <mxGeometry x="310" y="480" width="120" height="50" as="geometry"/>
    </mxCell>

    <!-- 连线 -->
    <mxCell id="20" value="" style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=1;" edge="1" parent="1" source="10" target="11"><mxGeometry relative="1" as="geometry"/></mxCell>
    <mxCell id="21" value="" style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=1;" edge="1" parent="1" source="11" target="12"><mxGeometry relative="1" as="geometry"/></mxCell>
    <mxCell id="22" value="是" style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=1;fontSize=11;" edge="1" parent="1" source="12" target="13"><mxGeometry relative="1" as="geometry"/></mxCell>
    <mxCell id="23" value="否" style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=1;fontSize=11;exitX=1;exitY=0.5;" edge="1" parent="1" source="12" target="14"><mxGeometry relative="1" as="geometry"/></mxCell>
    <mxCell id="24" value="" style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=1;" edge="1" parent="1" source="13" target="15"><mxGeometry relative="1" as="geometry"/></mxCell>

  </root>
</mxGraphModel>
```