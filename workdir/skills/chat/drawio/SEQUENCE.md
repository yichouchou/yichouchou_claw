# TYPE 3: 时序图

## 需要向用户问清楚的信息

- 参与者及其出现顺序
- 消息(带编号、类型:同步/异步/返回)
- 条件块(alt/opt/loop)

## Sequence checklist

- ID 连续递增(无跳号)
- Lifeline 用 dashed 样式
- Activation box 与消息对齐
- 箭头样式正确
- 最小间距 120px/100px

## 最小 Sequence 模板

```xml
<mxGraphModel page="1" pageWidth="1169" pageHeight="827">
  <root>
    <mxCell id="0"/>
    <mxCell id="1" parent="0"/>
    <mxCell id="10" value=":用户" style="shape=mxgraph.flowchart.start_2;" vertex="1" parent="1">
      <mxGeometry x="80" y="40" width="50" height="60" as="geometry"/>
    </mxCell>
    <mxCell id="11" value=":服务" style="rounded=0;" vertex="1" parent="1">
      <mxGeometry x="220" y="50" width="140" height="40" as="geometry"/>
    </mxCell>
    <mxCell id="20" value="" style="dashed=1;endArrow=none;" edge="1" parent="1" source="10">
      <mxGeometry relative="1" as="geometry">
        <Array as="points"><mxPoint x="105" y="100"/><mxPoint x="105" y="500"/></Array>
      </mxGeometry>
    </mxCell>
    <mxCell id="21" value="" style="dashed=1;endArrow=none;" edge="1" parent="1" source="11">
      <mxGeometry relative="1" as="geometry">
        <Array as="points"><mxPoint x="290" y="90"/><mxPoint x="290" y="500"/></Array>
      </mxGeometry>
    </mxCell>
    <mxCell id="30" value="1: request()" style="endArrow=block;endFill=1;" edge="1" parent="1">
      <mxGeometry relative="1" as="geometry">
        <Array as="points"><mxPoint x="105" y="160"/><mxPoint x="290" y="160"/></Array>
      </mxGeometry>
    </mxCell>
  </root>
</mxGraphModel>
```

## 布局规则

- 参与者从左到右排列
- 消息从上到下
- Frame 覆盖消息范围

## 常见错误

- Lifeline 没有 dashed
- Activation 没对齐
- 返回箭头没 dashed

## 何时反问用户

- 消息类型未指明
- 参与者缺失

## 视觉参考

时序图必须包含:

- **Actor** 放最左边(火柴人 / person shape)
- **Lifeline** 在每个参与者头下方的垂直虚线
- **Activation box**(窄长方形)叠在 lifeline 上,表示参与者激活期间
- **消息箭头** — 实心填充箭头=同步调用,虚线开放箭头=返回
- **Alt/Loop/Opt frame** 作为大容器圈出条件段
- 消息编号遵循 UML 规范(1, 1.1, 1.2, 1.2.1 ...)

## 参与者头 shape

| 参与者类型 | Shape style |
|---|---|
| 人类 actor | `shape=mxgraph.flowchart.start_2`,label 在下方 |
| 对象 / 类实例 | 普通矩形 `rounded=0` |
| 数据库 | `shape=cylinder3` |
| 边界 / UI | 椭圆 或 矩形 |
| 外部系统 | `rounded=1` 不同颜色 |

## 消息箭头样式

```xml
<!-- 同步调用(实心填充箭头) -->
style="endArrow=block;endFill=1;startArrow=none;edgeStyle=orthogonalEdgeStyle;"

<!-- 返回 / 响应(虚线开放箭头) -->
style="endArrow=open;endFill=0;dashed=1;startArrow=none;edgeStyle=orthogonalEdgeStyle;"

<!-- 异步消息(开放箭头) -->
style="endArrow=open;endFill=0;startArrow=none;edgeStyle=orthogonalEdgeStyle;"

<!-- 自调用(从同 x 位置向右再返回;用 geometry Array 显式 waypoints) -->
```

## 完整 XML 模板

```xml
<mxGraphModel dx="1422" dy="762" grid="1" gridSize="10" guides="1" tooltips="1" connect="1" arrows="1" fold="1" page="1" pageScale="1" pageWidth="1169" pageHeight="827" math="0" shadow="0">
  <root>
    <mxCell id="0"/>
    <mxCell id="1" parent="0"/>

    <!-- PARTICIPANT 头部 -->

    <!-- Actor: 客户 -->
    <mxCell id="10" value=":客户"
      style="shape=mxgraph.flowchart.start_2;fillColor=#f5f5f5;strokeColor=#666666;fontColor=#333333;fontSize=12;fontStyle=1;align=center;verticalLabelPosition=bottom;verticalAlign=top;"
      vertex="1" parent="1">
      <mxGeometry x="60" y="40" width="50" height="60" as="geometry"/>
    </mxCell>

    <!-- Object: 搜索表单 -->
    <mxCell id="11" value=":搜索表单"
      style="rounded=0;whiteSpace=wrap;html=1;fillColor=#FFE6CC;strokeColor=#d79b00;fontStyle=1;fontSize=13;align=center;"
      vertex="1" parent="1">
      <mxGeometry x="220" y="50" width="140" height="40" as="geometry"/>
    </mxCell>

    <!-- Object: 搜索结果 -->
    <mxCell id="12" value=":搜索结果"
      style="rounded=0;whiteSpace=wrap;html=1;fillColor=#dae8fc;strokeColor=#6c8ebf;fontStyle=1;fontSize=13;align=center;"
      vertex="1" parent="1">
      <mxGeometry x="440" y="50" width="140" height="40" as="geometry"/>
    </mxCell>

    <!-- Object: 商品库 (圆 / boundary) -->
    <mxCell id="13" value=":商品库"
      style="ellipse;whiteSpace=wrap;html=1;fillColor=#e1d5e7;strokeColor=#9673a6;fontStyle=1;fontSize=12;align=center;"
      vertex="1" parent="1">
      <mxGeometry x="650" y="35" width="130" height="60" as="geometry"/>
    </mxCell>

    <!-- Object: 结果列表 -->
    <mxCell id="14" value=":结果列表"
      style="rounded=0;whiteSpace=wrap;html=1;fillColor=#d5e8d4;strokeColor=#82b366;fontStyle=1;fontSize=13;align=center;"
      vertex="1" parent="1">
      <mxGeometry x="850" y="50" width="120" height="40" as="geometry"/>
    </mxCell>

    <!-- LIFELINES (垂直虚线) -->

    <mxCell id="20" value=""
      style="edgeStyle=none;dashed=1;endArrow=none;startArrow=none;strokeColor=#666666;exitX=0.5;exitY=1;exitDx=0;exitDy=0;"
      edge="1" parent="1" source="10">
      <mxGeometry relative="1" as="geometry">
        <Array as="points">
          <mxPoint x="85" y="100"/>
          <mxPoint x="85" y="640"/>
        </Array>
      </mxGeometry>
    </mxCell>

    <mxCell id="21" value=""
      style="edgeStyle=none;dashed=1;endArrow=none;startArrow=none;strokeColor=#d79b00;exitX=0.5;exitY=1;exitDx=0;exitDy=0;"
      edge="1" parent="1" source="11">
      <mxGeometry relative="1" as="geometry">
        <Array as="points">
          <mxPoint x="290" y="90"/>
          <mxPoint x="290" y="640"/>
        </Array>
      </mxGeometry>
    </mxCell>

    <mxCell id="22" value=""
      style="edgeStyle=none;dashed=1;endArrow=none;startArrow=none;strokeColor=#6c8ebf;exitX=0.5;exitY=1;exitDx=0;exitDy=0;"
      edge="1" parent="1" source="12">
      <mxGeometry relative="1" as="geometry">
        <Array as="points">
          <mxPoint x="510" y="90"/>
          <mxPoint x="510" y="640"/>
        </Array>
      </mxGeometry>
    </mxCell>

    <mxCell id="23" value=""
      style="edgeStyle=none;dashed=1;endArrow=none;startArrow=none;strokeColor=#9673a6;exitX=0.5;exitY=1;exitDx=0;exitDy=0;"
      edge="1" parent="1" source="13">
      <mxGeometry relative="1" as="geometry">
        <Array as="points">
          <mxPoint x="715" y="95"/>
          <mxPoint x="715" y="640"/>
        </Array>
      </mxGeometry>
    </mxCell>

    <mxCell id="24" value=""
      style="edgeStyle=none;dashed=1;endArrow=none;startArrow=none;strokeColor=#82b366;exitX=0.5;exitY=1;exitDx=0;exitDy=0;"
      edge="1" parent="1" source="14">
      <mxGeometry relative="1" as="geometry">
        <Array as="points">
          <mxPoint x="910" y="90"/>
          <mxPoint x="910" y="640"/>
        </Array>
      </mxGeometry>
    </mxCell>

    <!-- ACTIVATION BOXES -->

    <mxCell id="30" value="" style="fillColor=#f5f5f5;strokeColor=#666666;" vertex="1" parent="1">
      <mxGeometry x="78" y="135" width="14" height="400" as="geometry"/>
    </mxCell>

    <mxCell id="31" value="" style="fillColor=#FFE6CC;strokeColor=#d79b00;" vertex="1" parent="1">
      <mxGeometry x="283" y="155" width="14" height="360" as="geometry"/>
    </mxCell>

    <mxCell id="32" value="" style="fillColor=#e1d5e7;strokeColor=#9673a6;" vertex="1" parent="1">
      <mxGeometry x="708" y="235" width="14" height="90" as="geometry"/>
    </mxCell>

    <mxCell id="33" value="" style="fillColor=#d5e8d4;strokeColor=#82b366;" vertex="1" parent="1">
      <mxGeometry x="903" y="255" width="14" height="60" as="geometry"/>
    </mxCell>

    <!-- ALT FRAME -->
    <mxCell id="40" value="alt"
      style="swimlane;startSize=20;swimlaneLine=1;fillColor=none;strokeColor=#666666;align=left;spacingLeft=5;fontSize=12;fontStyle=1;"
      vertex="1" parent="1">
      <mxGeometry x="120" y="168" width="840" height="360" as="geometry"/>
    </mxCell>

    <!-- Alt 条件文本 -->
    <mxCell id="41" value="[itemName=valid]"
      style="text;strokeColor=none;fillColor=none;align=left;fontSize=11;fontStyle=2;"
      vertex="1" parent="1">
      <mxGeometry x="125" y="172" width="160" height="20" as="geometry"/>
    </mxCell>

    <!-- Else 分隔线 -->
    <mxCell id="42" value=""
      style="edgeStyle=none;dashed=1;endArrow=none;startArrow=none;strokeColor=#666666;"
      edge="1" parent="1">
      <mxGeometry relative="1" as="geometry">
        <Array as="points">
          <mxPoint x="120" y="400"/>
          <mxPoint x="960" y="400"/>
        </Array>
      </mxGeometry>
    </mxCell>
    <mxCell id="43" value="[else]"
      style="text;strokeColor=none;fillColor=none;align=left;fontSize=11;fontStyle=2;"
      vertex="1" parent="1">
      <mxGeometry x="125" y="402" width="80" height="20" as="geometry"/>
    </mxCell>

    <!-- 消息箭头 -->

    <!-- 1: itemSearch(itemName) 客户 -> 搜索表单 -->
    <mxCell id="50" value="1: itemSearch(itemName)"
      style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=1;startArrow=none;fontSize=11;"
      edge="1" parent="1">
      <mxGeometry relative="1" as="geometry">
        <Array as="points">
          <mxPoint x="92" y="155"/>
          <mxPoint x="283" y="155"/>
        </Array>
      </mxGeometry>
    </mxCell>

    <!-- 1.1: validSearch() 搜索表单 自调用 -->
    <mxCell id="51" value="1.1: validSearch()"
      style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=1;startArrow=none;fontSize=11;"
      edge="1" parent="1">
      <mxGeometry relative="1" as="geometry">
        <Array as="points">
          <mxPoint x="297" y="190"/>
          <mxPoint x="340" y="190"/>
          <mxPoint x="340" y="215"/>
          <mxPoint x="297" y="215"/>
        </Array>
      </mxGeometry>
    </mxCell>

    <!-- 1.2: SearchItems(itemName) 搜索表单 -> 商品库 -->
    <mxCell id="52" value="1.2: SearchItems(itemName)"
      style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=1;startArrow=none;fontSize=11;"
      edge="1" parent="1">
      <mxGeometry relative="1" as="geometry">
        <Array as="points">
          <mxPoint x="297" y="240"/>
          <mxPoint x="708" y="240"/>
        </Array>
      </mxGeometry>
    </mxCell>

    <!-- 1.2.1: listResults() 商品库 -> 结果列表 -->
    <mxCell id="53" value="1.2.1: listResults()"
      style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=1;startArrow=none;fontSize=11;"
      edge="1" parent="1">
      <mxGeometry relative="1" as="geometry">
        <Array as="points">
          <mxPoint x="722" y="265"/>
          <mxPoint x="903" y="265"/>
        </Array>
      </mxGeometry>
    </mxCell>

    <!-- 1.2.1.1: displayResults() 结果列表 -> 搜索结果 (返回虚线) -->
    <mxCell id="54" value="1.2.1.1: displayResults()"
      style="edgeStyle=orthogonalEdgeStyle;endArrow=open;endFill=0;dashed=1;startArrow=none;fontSize=11;"
      edge="1" parent="1">
      <mxGeometry relative="1" as="geometry">
        <Array as="points">
          <mxPoint x="903" y="300"/>
          <mxPoint x="510" y="300"/>
        </Array>
      </mxGeometry>
    </mxCell>

    <!-- 1.3: displayError() 搜索表单 -> 客户 (else 分支) -->
    <mxCell id="55" value="1.3: displayError()"
      style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=1;startArrow=none;fontSize=11;"
      edge="1" parent="1">
      <mxGeometry relative="1" as="geometry">
        <Array as="points">
          <mxPoint x="283" y="430"/>
          <mxPoint x="92" y="430"/>
        </Array>
      </mxGeometry>
    </mxCell>

    <!-- 返回客户(虚线箭头) -->
    <mxCell id="56" value=""
      style="edgeStyle=orthogonalEdgeStyle;endArrow=open;endFill=0;dashed=1;startArrow=none;fontSize=11;"
      edge="1" parent="1">
      <mxGeometry relative="1" as="geometry">
        <Array as="points">
          <mxPoint x="283" y="510"/>
          <mxPoint x="92" y="510"/>
        </Array>
      </mxGeometry>
    </mxCell>

  </root>
</mxGraphModel>
```