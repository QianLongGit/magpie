# vision-route middleware（按请求的视觉模型路由）

从 cc-switch 私有 fork 移植的网关中间件：请求中**最新 user 消息**带图片
（含 tool_result 内嵌、stringified JSON、data URL）时，该请求的模型改写为
配置的视觉模型；历史图片不触发。绑定到路由组（`model` 以 `group/` 开头）
的请求不改写——组是「唯一出口」，改写会击穿项目绑定。

## 安装（私有 fork 形态：手工安装，单一来源在 testdata）

```sh
mkdir -p ~/.config/magpie/middleware
cp internal/middleware/testdata/vision-route.middleware.js \
   ~/.config/magpie/middleware/vision-route.middleware.js
MW=~/.config/magpie/middleware/vision-route.middleware.js
magpie plugin add "$MW"
magpie plugin options "$MW" '{"model":"zhipu/glm-5.3-flash"}'
```

（路径安装的中间件没有短名——`plugin options` 用完整路径，实测
`magpie plugin options vision-route` 报 no plugin。）

- 测试与安装共用 `internal/middleware/testdata/vision-route.middleware.js`
  这一份源文件（防漂移）；fork 更新脚本后需重新 `cp` 一次并重载
  （plugins.json 或脚本变更下一请求至多 1s 生效，无需重启网关）。
- 直接 `magpie plugin add ./internal/middleware/testdata/vision-route.middleware.js`
  （仓库路径）也可用，但仓库移动/清理会让绝对路径失效，推荐拷贝到
  `~/.config/magpie/middleware/`。

## 配置

`magpie plugin options <中间件完整路径> '{"model":"<id>"}'`

- 推荐生产值 `zhipu/glm-5.3-flash`（provider 钉死；图片轮换用便宜
  flash，正文 glm-5.3——catalog 核实 flash 原生多模态、glm-5.3 纯文本）。
- 裸名 / `group/<id>` 也原样接受，网关自行解析。
- ⚠ 别写大写 `[1M]` 后缀：magpie 的窗口标记是小写 `[1m]`（TrimSuffix
  大小写敏感），大写形态不被剥离、原样发上游会 400/404。中间件对配置值
  不做纠正，写错的责任在本文档与配置者。
- 判定的 8KB data URL 门槛按 JS 字符长度（UTF-16 码元）计，与 fork 的
  字节数在含多字节字符的伪 data URL 上有微小偏差（magpie 侧更保守、
  偏 fail-open 方向；真实 base64 纯 ASCII 两者一致）。
- 未配置（options 为空/缺 model）时恒不触发，行为与未安装一致。

## 与其他中间件的次序

改写型中间件按 plugins.json 的条目顺序依次执行（前一个的输出是后一个的
输入）。若同装多个改写 `body.model` 的中间件，把 vision-route 放在
**最前**（它只在带图请求上触发，先判图片语义再做其他映射，语义最稳）；
`group/*` 守卫保证它不会击穿任何后续绑定类改写。
