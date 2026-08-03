# TYPE 1: ERD / 数据库图

## 需要向用户问清楚的信息

- 表清单(列 + 类型)
- 主键 / 外键 / 基数(1:1, 1:N, N:M)
- 关键业务约束(唯一性、可空性)
- 是否有 lookup 表或 junction 表

## ERD checklist

- `mxCell` ID 连续递增(无跳号)
- 高度公式: `30 + (列数 × 30)`
- FK 关系连接到行 ID,不是表的容器 ID
- `value` 里禁用 `\n`
- 最小间距: 水平 120px / 垂直 100px

## 最小 ERD 模板

```xml
<mxGraphModel page="1" pageWidth="1169" pageHeight="827">
  <root>
    <mxCell id="0"/>
    <mxCell id="1" parent="0"/>
    <mxCell id="10" value="表名"
      style="shape=table;startSize=30;container=1;collapsible=0;childLayout=tableLayout;fixedRows=1;rowLines=0;fontStyle=1;fontSize=14;align=center;fillColor=#dae8fc;strokeColor=#6c8ebf;"
      vertex="1" parent="1">
      <mxGeometry x="80" y="80" width="220" height="90" as="geometry"/>
    </mxCell>
    <mxCell id="11" value="" style="shape=tableRow;horizontal=0;bottom=1;" vertex="1" parent="10">
      <mxGeometry y="30" width="220" height="30" as="geometry"/>
    </mxCell>
    <mxCell id="12" value="PK" style="shape=partialRectangle;fillColor=#fff2cc;strokeColor=#d6b656;" vertex="1" parent="11">
      <mxGeometry width="40" height="30" as="geometry"/>
    </mxCell>
    <mxCell id="13" value="id INT" style="shape=partialRectangle;fillColor=none;" vertex="1" parent="11">
      <mxGeometry x="40" width="180" height="30" as="geometry"/>
    </mxCell>
  </root>
</mxGraphModel>
```

## 布局规则

- 适用 4 表网格布局
- 最小间距 120px/100px
- 关系线不要穿过表头

## 常见错误

- 关系连接到容器而不是行
- 容器高度计算错误
- ID 复用
- 使用通用矩形代替表格

## 何时反问用户

- 基数未指定
- FK 未识别
- 列缺类型

## 视觉参考

数据库表应**看起来像真正的数据库表**:

- **表头行**(带背景色,显示表名)
- **每列一行**,左侧带 PK/FK 标记
- **关系线**用 crow's foot 记号从 FK 行连到 PK 行

## 表高度公式

```
table_height = 30 (header) + (列数 x 30)
示例: 5 列 -> 高度 = 30 + (5 x 30) = 180
```

## 表类型配色

| 表角色 | fillColor | strokeColor |
|---|---|---|
| 主实体 / primary | `#dae8fc` | `#6c8ebf` |
| 次实体 / secondary | `#e1d5e7` | `#9673a6` |
| 关联 / junction | `#d5e8d4` | `#82b366` |
| 字典 / lookup | `#fff2cc` | `#d6b656` |

## 列标记配色

| 标记 | fillColor | strokeColor |
|---|---|---|
| PK | `#fff2cc` | `#d6b656` |
| FK | `#f8cecc` | `#b85450` |
| UK (unique) | `#d5e8d4` | `#82b366` |
| (无) | `none` | `none` |

## 完整 XML 模板

```xml
<mxGraphModel dx="1422" dy="762" grid="1" gridSize="10" guides="1" tooltips="1" connect="1" arrows="1" fold="1" page="1" pageScale="1" pageWidth="1169" pageHeight="827" math="0" shadow="0">
  <root>
    <mxCell id="0"/>
    <mxCell id="1" parent="0"/>

    <!-- TABLE: 客户 | height = 30 + (5 x 30) = 180 -->
    <mxCell id="10" value="客户"
      style="shape=table;startSize=30;container=1;collapsible=0;childLayout=tableLayout;fixedRows=1;rowLines=0;fontStyle=1;fontSize=14;align=center;fillColor=#dae8fc;strokeColor=#6c8ebf;fontColor=#000000;swimlaneLine=1;"
      vertex="1" parent="1">
      <mxGeometry x="80" y="80" width="220" height="180" as="geometry"/>
    </mxCell>

    <!-- Row: ID (PK) - bottom=1 在 PK 后加分隔线 -->
    <mxCell id="11" value=""
      style="shape=tableRow;horizontal=0;startSize=0;swimlaneHead=0;swimlaneBody=0;fillColor=#dae8fc;collapsible=0;dropTarget=0;points=[[0,0.5],[1,0.5]];portConstraint=eastwest;fontSize=12;top=0;left=0;right=0;bottom=1;"
      vertex="1" parent="10">
      <mxGeometry y="30" width="220" height="30" as="geometry"/>
    </mxCell>
    <mxCell id="12" value="PK"
      style="shape=partialRectangle;connectable=0;fillColor=#fff2cc;strokeColor=#d6b656;top=0;left=0;bottom=0;right=0;fontStyle=1;fontSize=11;overflow=hidden;align=center;"
      vertex="1" parent="11">
      <mxGeometry width="40" height="30" as="geometry"><mxRectangle width="40" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>
    <mxCell id="13" value="ID INT"
      style="shape=partialRectangle;connectable=0;fillColor=none;top=0;left=0;bottom=0;right=0;overflow=hidden;fontSize=12;"
      vertex="1" parent="11">
      <mxGeometry x="40" width="180" height="30" as="geometry"><mxRectangle width="180" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>

    <!-- Row: 姓名 -->
    <mxCell id="14" value=""
      style="shape=tableRow;horizontal=0;startSize=0;swimlaneHead=0;swimlaneBody=0;fillColor=none;collapsible=0;dropTarget=0;points=[[0,0.5],[1,0.5]];portConstraint=eastwest;fontSize=12;top=0;left=0;right=0;bottom=0;"
      vertex="1" parent="10">
      <mxGeometry y="60" width="220" height="30" as="geometry"/>
    </mxCell>
    <mxCell id="15" value=""
      style="shape=partialRectangle;connectable=0;fillColor=none;top=0;left=0;bottom=0;right=0;overflow=hidden;"
      vertex="1" parent="14">
      <mxGeometry width="40" height="30" as="geometry"><mxRectangle width="40" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>
    <mxCell id="16" value="姓名 VARCHAR(100)"
      style="shape=partialRectangle;connectable=0;fillColor=none;top=0;left=0;bottom=0;right=0;overflow=hidden;fontSize=12;"
      vertex="1" parent="14">
      <mxGeometry x="40" width="180" height="30" as="geometry"><mxRectangle width="180" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>

    <!-- Row: 手机 -->
    <mxCell id="17" value=""
      style="shape=tableRow;horizontal=0;startSize=0;swimlaneHead=0;swimlaneBody=0;fillColor=none;collapsible=0;dropTarget=0;points=[[0,0.5],[1,0.5]];portConstraint=eastwest;fontSize=12;top=0;left=0;right=0;bottom=0;"
      vertex="1" parent="10">
      <mxGeometry y="90" width="220" height="30" as="geometry"/>
    </mxCell>
    <mxCell id="18" value=""
      style="shape=partialRectangle;connectable=0;fillColor=none;top=0;left=0;bottom=0;right=0;overflow=hidden;"
      vertex="1" parent="17">
      <mxGeometry width="40" height="30" as="geometry"><mxRectangle width="40" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>
    <mxCell id="19" value="手机 VARCHAR(20)"
      style="shape=partialRectangle;connectable=0;fillColor=none;top=0;left=0;bottom=0;right=0;overflow=hidden;fontSize=12;"
      vertex="1" parent="17">
      <mxGeometry x="40" width="180" height="30" as="geometry"><mxRectangle width="180" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>

    <!-- Row: 邮箱 -->
    <mxCell id="20" value=""
      style="shape=tableRow;horizontal=0;startSize=0;swimlaneHead=0;swimlaneBody=0;fillColor=none;collapsible=0;dropTarget=0;points=[[0,0.5],[1,0.5]];portConstraint=eastwest;fontSize=12;top=0;left=0;right=0;bottom=0;"
      vertex="1" parent="10">
      <mxGeometry y="120" width="220" height="30" as="geometry"/>
    </mxCell>
    <mxCell id="21" value=""
      style="shape=partialRectangle;connectable=0;fillColor=none;top=0;left=0;bottom=0;right=0;overflow=hidden;"
      vertex="1" parent="20">
      <mxGeometry width="40" height="30" as="geometry"><mxRectangle width="40" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>
    <mxCell id="22" value="邮箱 VARCHAR(150)"
      style="shape=partialRectangle;connectable=0;fillColor=none;top=0;left=0;bottom=0;right=0;overflow=hidden;fontSize=12;"
      vertex="1" parent="20">
      <mxGeometry x="40" width="180" height="30" as="geometry"><mxRectangle width="180" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>

    <!-- Row: 注册时间 -->
    <mxCell id="23" value=""
      style="shape=tableRow;horizontal=0;startSize=0;swimlaneHead=0;swimlaneBody=0;fillColor=none;collapsible=0;dropTarget=0;points=[[0,0.5],[1,0.5]];portConstraint=eastwest;fontSize=12;top=0;left=0;right=0;bottom=0;"
      vertex="1" parent="10">
      <mxGeometry y="150" width="220" height="30" as="geometry"/>
    </mxCell>
    <mxCell id="24" value=""
      style="shape=partialRectangle;connectable=0;fillColor=none;top=0;left=0;bottom=0;right=0;overflow=hidden;"
      vertex="1" parent="23">
      <mxGeometry width="40" height="30" as="geometry"><mxRectangle width="40" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>
    <mxCell id="25" value="注册时间 DATETIME"
      style="shape=partialRectangle;connectable=0;fillColor=none;top=0;left=0;bottom=0;right=0;overflow=hidden;fontSize=12;"
      vertex="1" parent="23">
      <mxGeometry x="40" width="180" height="30" as="geometry"><mxRectangle width="180" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>

    <!-- TABLE: 订单 | height = 30 + (3 x 30) = 120 -->
    <mxCell id="30" value="订单"
      style="shape=table;startSize=30;container=1;collapsible=0;childLayout=tableLayout;fixedRows=1;rowLines=0;fontStyle=1;fontSize=14;align=center;fillColor=#e1d5e7;strokeColor=#9673a6;fontColor=#000000;swimlaneLine=1;"
      vertex="1" parent="1">
      <mxGeometry x="80" y="380" width="220" height="120" as="geometry"/>
    </mxCell>

    <!-- Row: ID (PK) -->
    <mxCell id="31" value=""
      style="shape=tableRow;horizontal=0;startSize=0;swimlaneHead=0;swimlaneBody=0;fillColor=#e1d5e7;collapsible=0;dropTarget=0;points=[[0,0.5],[1,0.5]];portConstraint=eastwest;fontSize=12;top=0;left=0;right=0;bottom=1;"
      vertex="1" parent="30">
      <mxGeometry y="30" width="220" height="30" as="geometry"/>
    </mxCell>
    <mxCell id="32" value="PK"
      style="shape=partialRectangle;connectable=0;fillColor=#fff2cc;strokeColor=#d6b656;top=0;left=0;bottom=0;right=0;fontStyle=1;fontSize=11;overflow=hidden;align=center;"
      vertex="1" parent="31">
      <mxGeometry width="40" height="30" as="geometry"><mxRectangle width="40" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>
    <mxCell id="33" value="ID INT"
      style="shape=partialRectangle;connectable=0;fillColor=none;top=0;left=0;bottom=0;right=0;overflow=hidden;fontSize=12;"
      vertex="1" parent="31">
      <mxGeometry x="40" width="180" height="30" as="geometry"><mxRectangle width="180" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>

    <!-- Row: 日期 -->
    <mxCell id="34" value=""
      style="shape=tableRow;horizontal=0;startSize=0;swimlaneHead=0;swimlaneBody=0;fillColor=none;collapsible=0;dropTarget=0;points=[[0,0.5],[1,0.5]];portConstraint=eastwest;fontSize=12;top=0;left=0;right=0;bottom=0;"
      vertex="1" parent="30">
      <mxGeometry y="60" width="220" height="30" as="geometry"/>
    </mxCell>
    <mxCell id="35" value=""
      style="shape=partialRectangle;connectable=0;fillColor=none;top=0;left=0;bottom=0;right=0;overflow=hidden;"
      vertex="1" parent="34">
      <mxGeometry width="40" height="30" as="geometry"><mxRectangle width="40" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>
    <mxCell id="36" value="日期 DATE"
      style="shape=partialRectangle;connectable=0;fillColor=none;top=0;left=0;bottom=0;right=0;overflow=hidden;fontSize=12;"
      vertex="1" parent="34">
      <mxGeometry x="40" width="180" height="30" as="geometry"><mxRectangle width="180" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>

    <!-- Row: 客户ID (FK) -->
    <mxCell id="37" value=""
      style="shape=tableRow;horizontal=0;startSize=0;swimlaneHead=0;swimlaneBody=0;fillColor=none;collapsible=0;dropTarget=0;points=[[0,0.5],[1,0.5]];portConstraint=eastwest;fontSize=12;top=0;left=0;right=0;bottom=0;"
      vertex="1" parent="30">
      <mxGeometry y="90" width="220" height="30" as="geometry"/>
    </mxCell>
    <mxCell id="38" value="FK"
      style="shape=partialRectangle;connectable=0;fillColor=#f8cecc;strokeColor=#b85450;top=0;left=0;bottom=0;right=0;fontStyle=1;fontSize=11;overflow=hidden;align=center;"
      vertex="1" parent="37">
      <mxGeometry width="40" height="30" as="geometry"><mxRectangle width="40" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>
    <mxCell id="39" value="客户ID INT"
      style="shape=partialRectangle;connectable=0;fillColor=none;top=0;left=0;bottom=0;right=0;overflow=hidden;fontSize=12;"
      vertex="1" parent="37">
      <mxGeometry x="40" width="180" height="30" as="geometry"><mxRectangle width="180" height="30" as="alternateBounds"/></mxGeometry>
    </mxCell>

    <!-- RELATIONSHIP: 客户 1 to N 订单 -->
    <!-- source=订单的 FK 行, target=客户的 PK 行 -->
    <mxCell id="90" value=""
      style="edgeStyle=entityRelationEdgeStyle;endArrow=ERmandOne;startArrow=ERzeroToMany;exitX=0;exitY=0.5;exitDx=0;exitDy=0;entryX=0;entryY=0.5;entryDx=0;entryDy=0;fontSize=11;"
      edge="1" parent="1" source="37" target="11">
      <mxGeometry relative="1" as="geometry"/>
    </mxCell>

  </root>
</mxGraphModel>
```

## ERD 关系样式(crow's foot)

```xml
<!-- One-to-Many (1:N) -->
style="edgeStyle=entityRelationEdgeStyle;endArrow=ERmandOne;startArrow=ERzeroToMany;"

<!-- One-to-Many mandatory -->
style="edgeStyle=entityRelationEdgeStyle;endArrow=ERmandOne;startArrow=ERmandMany;"

<!-- Zero-or-One to Many -->
style="edgeStyle=entityRelationEdgeStyle;endArrow=ERzeroToOne;startArrow=ERzeroToMany;"

<!-- Many-to-Many (N:M) -->
style="edgeStyle=entityRelationEdgeStyle;endArrow=ERzeroToMany;startArrow=ERzeroToMany;"
```

## ERD 4 表网格布局

```
表 A (primary):   x=80,  y=80
表 B (primary):   x=420, y=80
表 C (secondary): x=80,  y=380
表 D (junction):  x=420, y=380
```

5+ 表时,在 `mxGraphModel` 里把 `pageWidth="1654" pageHeight="1169"`。