# TYPE 2: UML 类图

## 需要向用户问清楚的信息

- 类清单(属性 + 方法)
- 可见性 (+/-/#/~) 和类型
- 继承、实现、关联和多重性

## Class checklist

- ID 连续递增(无跳号)
- 三个分区都可见
- 属性和方法之间有分隔线
- `value` 里禁用 `\n`(用 `&#xa;`)
- 最小间距 120px/100px

## 最小 Class 模板

```xml
<mxGraphModel page="1" pageWidth="1169" pageHeight="827">
  <root>
    <mxCell id="0"/>
    <mxCell id="1" parent="0"/>
    <mxCell id="10" value="类名"
      style="swimlane;fontStyle=1;align=center;startSize=30;fontSize=14;fillColor=#dae8fc;strokeColor=#6c8ebf;"
      vertex="1" parent="1">
      <mxGeometry x="80" y="80" width="220" height="120" as="geometry"/>
    </mxCell>
    <mxCell id="11" value="+attr: 类型"
      style="text;strokeColor=none;fillColor=none;align=left;verticalAlign=top;spacingLeft=4;"
      vertex="1" parent="10">
      <mxGeometry y="30" width="220" height="50" as="geometry"/>
    </mxCell>
    <mxCell id="12" value="" style="line;strokeWidth=1;strokeColor=#6c8ebf;" vertex="1" parent="10">
      <mxGeometry y="80" width="220" height="10" as="geometry"/>
    </mxCell>
    <mxCell id="13" value="+method(): 返回类型"
      style="text;strokeColor=none;fillColor=none;align=left;verticalAlign=top;spacingLeft=4;"
      vertex="1" parent="10">
      <mxGeometry y="90" width="220" height="30" as="geometry"/>
    </mxCell>
  </root>
</mxGraphModel>
```

## 布局规则

- 继承关系**从上到下**(父类在上)
- 关联放在两侧
- 保持按行对齐

## 常见错误

- 属性和方法之间没有分隔线
- 在 `value` 里用 `\n` 实现多行
- 关系连接到文本 cell

## 何时反问用户

- 多重性未指明
- 类型或可见性缺失

## 视觉参考

类应有三个分区:

1. **类名** — 顶部,加粗,带背景色
2. **属性** — 中间,带可见性前缀 (+/-/#) 和类型
3. **方法** — 底部,带可见性前缀和返回类型

## 可见性前缀

| 符号 | 含义 |
|---|---|
| `+` | public |
| `-` | private |
| `#` | protected |
| `~` | package |

## 类角色配色

| 角色 | fillColor | strokeColor |
|---|---|---|
| 普通类 | `#dae8fc` | `#6c8ebf` |
| 抽象类 | `#e1d5e7` | `#9673a6` |
| 接口 | `#d5e8d4` | `#82b366` |
| 枚举 | `#fff2cc` | `#d6b656` |

## 完整 XML 模板

```xml
<mxGraphModel dx="1422" dy="762" grid="1" gridSize="10" guides="1" tooltips="1" connect="1" arrows="1" fold="1" page="1" pageScale="1" pageWidth="1169" pageHeight="827" math="0" shadow="0">
  <root>
    <mxCell id="0"/>
    <mxCell id="1" parent="0"/>

    <!-- CLASS: 地址 -->
    <mxCell id="10" value="地址"
      style="swimlane;fontStyle=1;align=center;startSize=30;fontSize=14;fillColor=#dae8fc;strokeColor=#6c8ebf;"
      vertex="1" parent="1">
      <mxGeometry x="300" y="40" width="220" height="160" as="geometry"/>
    </mxCell>
    <!-- 属性(用 &#xa; 实现 cell 内换行) -->
    <mxCell id="11" value="+String 街道&#xa;+String 城市&#xa;+String 省&#xa;+int 邮编&#xa;+String 国家"
      style="text;strokeColor=none;fillColor=none;align=left;verticalAlign=top;spacingLeft=4;spacingRight=4;overflow=hidden;rotatable=0;fontSize=12;"
      vertex="1" parent="10">
      <mxGeometry y="30" width="220" height="100" as="geometry"/>
    </mxCell>
    <!-- 属性和方法之间的分隔线 -->
    <mxCell id="12" value=""
      style="line;strokeWidth=1;fillColor=none;align=left;strokeColor=#6c8ebf;"
      vertex="1" parent="10">
      <mxGeometry y="130" width="220" height="10" as="geometry"/>
    </mxCell>
    <!-- 方法 -->
    <mxCell id="13" value="-validate()&#xa;+outputAsLabel()"
      style="text;strokeColor=none;fillColor=none;align=left;verticalAlign=top;spacingLeft=4;spacingRight=4;overflow=hidden;rotatable=0;fontSize=12;"
      vertex="1" parent="10">
      <mxGeometry y="140" width="220" height="50" as="geometry"/>
    </mxCell>

    <!-- CLASS: 人 -->
    <mxCell id="20" value="人"
      style="swimlane;fontStyle=1;align=center;startSize=30;fontSize=14;fillColor=#dae8fc;strokeColor=#6c8ebf;"
      vertex="1" parent="1">
      <mxGeometry x="300" y="280" width="220" height="140" as="geometry"/>
    </mxCell>
    <mxCell id="21" value="+String 姓名&#xa;+int 电话&#xa;+String 邮箱"
      style="text;strokeColor=none;fillColor=none;align=left;verticalAlign=top;spacingLeft=4;spacingRight=4;overflow=hidden;rotatable=0;fontSize=12;"
      vertex="1" parent="20">
      <mxGeometry y="30" width="220" height="70" as="geometry"/>
    </mxCell>
    <mxCell id="22" value=""
      style="line;strokeWidth=1;fillColor=none;strokeColor=#6c8ebf;"
      vertex="1" parent="20">
      <mxGeometry y="100" width="220" height="10" as="geometry"/>
    </mxCell>
    <mxCell id="23" value="+purchaseParkingPass()"
      style="text;strokeColor=none;fillColor=none;align=left;verticalAlign=top;spacingLeft=4;spacingRight=4;overflow=hidden;rotatable=0;fontSize=12;"
      vertex="1" parent="20">
      <mxGeometry y="110" width="220" height="30" as="geometry"/>
    </mxCell>

    <!-- CLASS: 学生 (继承自 人) -->
    <mxCell id="30" value="学生"
      style="swimlane;fontStyle=1;align=center;startSize=30;fontSize=14;fillColor=#e1d5e7;strokeColor=#9673a6;"
      vertex="1" parent="1">
      <mxGeometry x="100" y="520" width="220" height="140" as="geometry"/>
    </mxCell>
    <mxCell id="31" value="+int 学号&#xa;+int 平均分"
      style="text;strokeColor=none;fillColor=none;align=left;verticalAlign=top;spacingLeft=4;spacingRight=4;overflow=hidden;rotatable=0;fontSize=12;"
      vertex="1" parent="30">
      <mxGeometry y="30" width="220" height="60" as="geometry"/>
    </mxCell>
    <mxCell id="32" value=""
      style="line;strokeWidth=1;fillColor=none;strokeColor=#9673a6;"
      vertex="1" parent="30">
      <mxGeometry y="90" width="220" height="10" as="geometry"/>
    </mxCell>
    <mxCell id="33" value="+isEligibleToEnroll()&#xa;+getSeminarsTaken()"
      style="text;strokeColor=none;fillColor=none;align=left;verticalAlign=top;spacingLeft=4;spacingRight=4;overflow=hidden;rotatable=0;fontSize=12;"
      vertex="1" parent="30">
      <mxGeometry y="100" width="220" height="50" as="geometry"/>
    </mxCell>

    <!-- CLASS: 教授 (继承自 人) -->
    <mxCell id="40" value="教授"
      style="swimlane;fontStyle=1;align=center;startSize=30;fontSize=14;fillColor=#e1d5e7;strokeColor=#9673a6;"
      vertex="1" parent="1">
      <mxGeometry x="500" y="520" width="220" height="100" as="geometry"/>
    </mxCell>
    <mxCell id="41" value="+int 薪资"
      style="text;strokeColor=none;fillColor=none;align=left;verticalAlign=top;spacingLeft=4;spacingRight=4;overflow=hidden;rotatable=0;fontSize=12;"
      vertex="1" parent="40">
      <mxGeometry y="30" width="220" height="30" as="geometry"/>
    </mxCell>
    <mxCell id="42" value=""
      style="line;strokeWidth=1;fillColor=none;strokeColor=#9673a6;"
      vertex="1" parent="40">
      <mxGeometry y="60" width="220" height="10" as="geometry"/>
    </mxCell>

    <!-- RELATIONSHIP: 地址 -> 人 (关联 "居住于" 0..1) -->
    <mxCell id="80" value="居住于"
      style="edgeStyle=orthogonalEdgeStyle;endArrow=open;endFill=0;startArrow=none;fontSize=11;"
      edge="1" parent="1" source="10" target="20">
      <mxGeometry relative="1" as="geometry"/>
    </mxCell>
    <mxCell id="81" value="0..1" style="resizable=0;align=left;verticalAlign=bottom;labelBackgroundColor=none;fontSize=11;" connectable="0" vertex="1" parent="80">
      <mxGeometry x="-0.7" relative="1" as="geometry"><mxPoint as="offset"/></mxGeometry>
    </mxCell>

    <!-- RELATIONSHIP: 学生 extends 人 (继承 - 空心三角) -->
    <mxCell id="82" value=""
      style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=0;startArrow=none;fontSize=11;"
      edge="1" parent="1" source="30" target="20">
      <mxGeometry relative="1" as="geometry"/>
    </mxCell>

    <!-- RELATIONSHIP: 教授 extends 人 (继承 - 空心三角) -->
    <mxCell id="83" value=""
      style="edgeStyle=orthogonalEdgeStyle;endArrow=block;endFill=0;startArrow=none;fontSize=11;"
      edge="1" parent="1" source="40" target="20">
      <mxGeometry relative="1" as="geometry"/>
    </mxCell>

  </root>
</mxGraphModel>
```

## 类图关系样式

```xml
<!-- 继承 / 泛化 (空心三角) -->
style="endArrow=block;endFill=0;startArrow=none;edgeStyle=orthogonalEdgeStyle;"

<!-- 接口实现 (虚线 + 空心三角) -->
style="endArrow=block;endFill=0;startArrow=none;dashed=1;edgeStyle=orthogonalEdgeStyle;"

<!-- 关联 (开放箭头) -->
style="endArrow=open;endFill=0;startArrow=none;edgeStyle=orthogonalEdgeStyle;"

<!-- 聚合 (空心菱形) -->
style="endArrow=open;startArrow=diamondThin;startFill=0;edgeStyle=orthogonalEdgeStyle;"

<!-- 组合 (实心菱形) -->
style="endArrow=open;startArrow=diamondThin;startFill=1;edgeStyle=orthogonalEdgeStyle;"

<!-- 依赖 (虚线开放箭头) -->
style="endArrow=open;endFill=0;dashed=1;edgeStyle=orthogonalEdgeStyle;"
```