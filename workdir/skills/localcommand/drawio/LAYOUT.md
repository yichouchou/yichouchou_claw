# TYPE 5: 架构图

## 需要向用户问清楚的信息

- 组件 / 服务及职责
- 协议或连接类型
- 系统边界(外部 / 内部)

## Architecture checklist

- ID 连续递增(无跳号)
- 图标风格统一
- 连线带协议标签
- 最小间距: 水平 220px / 垂直 180px
- 组件多时增大 `pageWidth`/`pageHeight`

## 最小 Architecture 模板

```xml
<mxGraphModel page="1" pageWidth="1169" pageHeight="827">
  <root>
    <mxCell id="0"/>
    <mxCell id="1" parent="0"/>
    <mxCell id="10" value="前端" style="rounded=1;" vertex="1" parent="1">
      <mxGeometry x="100" y="200" width="160" height="60" as="geometry"/>
    </mxCell>
    <mxCell id="11" value="API" style="rounded=1;" vertex="1" parent="1">
      <mxGeometry x="360" y="200" width="160" height="60" as="geometry"/>
    </mxCell>
    <mxCell id="20" value="HTTPS" style="endArrow=block;endFill=1;" edge="1" parent="1" source="10" target="11">
      <mxGeometry relative="1" as="geometry"/>
    </mxCell>
  </root>
</mxGraphModel>
```

## 布局规则

- 前端放左边,数据放右边
- 中央服务水平对齐
- 加大网格区分: `gridSize=20`
- 推荐间距: 列间 220px,行间 180px
- 超过 6 个组件,翻倍 page 宽

## 常见错误

- 连线缺协议标签
- 混用 shape 风格

## 何时反问用户

- 协议未指定
- 系统边界不清晰

## 组件示例 XML

```xml
<!-- 前端服务 -->
<mxCell id="10" value="React 前端"
  style="rounded=1;whiteSpace=wrap;html=1;fillColor=#dae8fc;strokeColor=#6c8ebf;fontSize=14;fontStyle=1;"
  vertex="1" parent="1">
  <mxGeometry x="100" y="200" width="160" height="60" as="geometry"/>
</mxCell>

<!-- API 网关 -->
<mxCell id="11" value="API 网关"
  style="rounded=1;whiteSpace=wrap;html=1;fillColor=#fff2cc;strokeColor=#d6b656;fontSize=14;fontStyle=1;"
  vertex="1" parent="1">
  <mxGeometry x="360" y="200" width="160" height="60" as="geometry"/>
</mxCell>

<!-- 微服务 -->
<mxCell id="12" value="认证服务"
  style="rounded=1;whiteSpace=wrap;html=1;fillColor=#d5e8d4;strokeColor=#82b366;fontSize=13;"
  vertex="1" parent="1">
  <mxGeometry x="620" y="120" width="140" height="60" as="geometry"/>
</mxCell>

<!-- 数据库圆柱 -->
<mxCell id="13" value="PostgreSQL"
  style="shape=cylinder3;whiteSpace=wrap;html=1;fillColor=#f5f5f5;strokeColor=#666666;fontSize=12;"
  vertex="1" parent="1">
  <mxGeometry x="640" y="300" width="110" height="80" as="geometry"/>
</mxCell>

<!-- 消息队列 -->
<mxCell id="14" value="RabbitMQ"
  style="shape=mxgraph.cisco.servers.standard_server;whiteSpace=wrap;html=1;fillColor=#e1d5e7;strokeColor=#9673a6;fontSize=12;"
  vertex="1" parent="1">
  <mxGeometry x="360" y="380" width="80" height="80" as="geometry"/>
</mxCell>

<!-- 带协议标签的连线 -->
<mxCell id="20" value="HTTPS"
  style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=1;fontSize=11;"
  edge="1" parent="1" source="10" target="11">
  <mxGeometry relative="1" as="geometry"/>
</mxCell>

<mxCell id="21" value="REST"
  style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=1;fontSize=11;"
  edge="1" parent="1" source="11" target="12">
  <mxGeometry relative="1" as="geometry"/>
</mxCell>

<mxCell id="22" value="SQL"
  style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=1;fontSize=11;"
  edge="1" parent="1" source="12" target="13">
  <mxGeometry relative="1" as="geometry"/>
</mxCell>
```

## 常见组件 shape 速查

| 组件类型 | Shape style |
|---|---|
| 普通服务 | `rounded=1;whiteSpace=wrap;html=1` |
| 数据库 | `shape=cylinder3` |
| 队列 / 缓存 | `shape=mxgraph.cisco.servers.standard_server` |
| 外部系统 | `rounded=1` + 不同配色(常用 `#f5f5f5`) |
| 用户 / Actor | `shape=mxgraph.flowchart.start_2` |
| 容器边界 | `swimlane` 套多个组件 |