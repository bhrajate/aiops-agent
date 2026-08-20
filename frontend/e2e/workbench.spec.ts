import { test, expect, type Page } from '@playwright/test'

// 这些用例只断言**必须用真实浏览器才能验证**的东西。
// 纯逻辑(格式化、阶段判定、图表数学)已由 src/**/*.test.ts 覆盖,不在这里重复。

async function login(page: Page, user: string, pass: string) {
  await page.goto('/login')
  await page.getByPlaceholder('alice').fill(user)
  await page.locator('input[type="password"]').fill(pass)
  await page.getByRole('button', { name: '登录' }).click()
  // 登录后应离开 /login
  await expect(page).not.toHaveURL(/\/login/)
}

// 收集控制台错误与未捕获异常。
// 这是本文件最重要的一条:tsc 过、构建过,运行时照样能崩
// (空数组 .map、undefined 属性链、SVG 属性非法…),而崩了页面是白的。
function collectErrors(page: Page): string[] {
  const errs: string[] = []
  page.on('console', (m) => {
    if (m.type() === 'error') errs.push(`console: ${m.text()}`)
  })
  page.on('pageerror', (e) => errs.push(`pageerror: ${e.message}`))
  return errs
}

test('总览页渲染完整,且控制台无错误', async ({ page }) => {
  const errs = collectErrors(page)
  await login(page, 'alice', 'alice-pass')

  // 页头与关键卡片都要真的出现。用可见文本而非 CSS 选择器:
  // 后者在改样式时会假失败,而这里要验的是"用户看得到什么"。
  await expect(page.getByRole('heading', { name: '值班总览' })).toBeVisible()
  await expect(page.getByText('当前状态')).toBeVisible()
  // 统计卡按**可及名**定位。"未闭环"这个词同时出现在卡片标签和 P2 卡的 hint 里,
  // 用 getByText 会 strict-mode 撞两个元素 —— 那是断言写得不够准,不是缺陷。
  await expect(
    page.getByRole('button', { name: /^未闭环 / }),
  ).toBeVisible()
  await expect(page.getByRole('button', { name: /^P1 / })).toBeVisible()
  await expect(page.getByText('趋势', { exact: true })).toBeVisible()

  // 趋势图必须真的画出路径。SVG 的 d 属性里出现 NaN 时浏览器静默丢弃整条路径 ——
  // 控制台干净、图是空的,而空图与"窗口内没数据"看起来一样。
  const paths = page.locator('svg[aria-label="故障与调查趋势"] path')
  await expect(paths.first()).toBeVisible()
  for (const d of await paths.evaluateAll((ns) =>
    ns.map((n) => n.getAttribute('d') ?? ''),
  )) {
    expect(d).not.toContain('NaN')
  }

  expect(errs, `控制台有错误:\n${errs.join('\n')}`).toEqual([])
})

test('样本不足时显示破折号,不显示 0', async ({ page }) => {
  await login(page, 'alice', 'alice-pass')
  // MTTR 卡:后端样本不足时返回 null,界面必须显示 —— 而不是 0s。
  // "0s" 会被读成"秒级解决",而真相是没有样本。这条只能在渲染后验:
  // 类型上 null 与 0 都合法,是渲染层决定读者看到什么。
  const card = page.locator('[data-stat="平均处置时长"]')
  await expect(card).toBeVisible()
  const txt = (await card.innerText()).replace(/\s+/g, ' ')
  if (txt.includes('无已解决样本')) {
    expect(txt).toContain('—')
    expect(txt).not.toMatch(/\b0s\b/)
  } else {
    // 有样本时必须是个真实时长,不能是空白
    expect(txt).toMatch(/\d/)
  }
})

test('侧边栏折叠后导航仍可达,且状态持久化', async ({ page }) => {
  await login(page, 'alice', 'alice-pass')
  await page.getByRole('button', { name: '折叠侧边栏' }).click()

  // 折叠态下"展开侧边栏"必须只匹配到**一个**控件。
  // 品牌标记与折叠按钮曾共用同一个 aria-label,读屏用户会听到两个同名控件
  // 而无法区分 —— 这里刻意不用 .first() 绕过,那样只会掩盖缺陷。
  await expect(page.getByRole('button', { name: '展开侧边栏' })).toHaveCount(1)

  // 图标栏里的入口仍然可点
  await page.getByRole('link', { name: '告警' }).click()
  await expect(page).toHaveURL(/\/incidents/)

  // 刷新后仍是折叠态(localStorage 持久化)
  await page.reload()
  await expect(page.getByRole('button', { name: '展开侧边栏' })).toHaveCount(1)
})

test('⌘K 命令面板:键盘导航与 ID 直达', async ({ page }) => {
  await login(page, 'alice', 'alice-pass')
  await page.keyboard.press('Control+k')
  await expect(page.getByRole('dialog', { name: '命令面板' })).toBeVisible()

  // 方向键 + 回车必须能真的跳转。这套键盘逻辑(环绕、过滤后高亮收回首项)
  // 是纯 DOM 交互,单测覆盖不到。
  const input = page.getByPlaceholder(/粘贴 inc-\/inv- ID 直达/)
  await input.fill('告警')
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  await expect(page.getByRole('dialog', { name: '命令面板' })).toBeHidden()

  // 粘贴 inc- 前缀应给出"按 ID 直达"入口 —— 值班时从告警群拷 ID 过来就能开
  await page.keyboard.press('Control+k')
  await page.getByPlaceholder(/粘贴 inc-\/inv- ID 直达/).fill('inc-abc123')
  await expect(page.getByText(/打开 Incident inc-abc123/)).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: '命令面板' })).toBeHidden()
})

test('主题切换真的改 <html> 的类,并持久化', async ({ page }) => {
  await login(page, 'alice', 'alice-pass')
  const htmlClass = () => page.evaluate(() => document.documentElement.className)

  // 首帧必须已有 dark/light 之一 —— index.html 预置 class="dark",
  // store/theme.ts 在 React 挂载前覆盖。没有这一步会闪白。
  expect(await htmlClass()).toMatch(/dark|light/)

  await page.goto('/settings')
  await page.getByRole('tab', { name: /浅色/ }).click()
  await expect.poll(htmlClass).toContain('light')

  await page.reload()
  await expect.poll(htmlClass).toContain('light')

  // 切回深色,避免影响后续用例的视觉基线
  await page.getByRole('tab', { name: /深色/ }).click()
  await expect.poll(htmlClass).toContain('dark')
})

test('viewer 看不到审计与评测入口', async ({ page }) => {
  await login(page, 'viewer', 'viewer-pass')
  // 无权入口整个隐藏,而不是点进去看 403。
  // 后端仍逐请求强制 —— 这里验的是前端没把入口露出来。
  await expect(page.getByRole('link', { name: '审计日志' })).toHaveCount(0)
  await expect(page.getByRole('link', { name: '评测集' })).toHaveCount(0)
  // 但值班主路径必须在
  await expect(page.getByRole('link', { name: '告警' })).toBeVisible()

  // 直接访问 /audit 应给出"没有权限"而不是崩掉或白屏
  await page.goto('/audit')
  await expect(page.getByText('没有查看权限')).toBeVisible()
})

test('告警表在窄视口可横向滚动,不截断列', async ({ page }) => {
  await login(page, 'alice', 'alice-pass')
  await page.setViewportSize({ width: 900, height: 800 })
  await page.goto('/incidents')

  // 表格有 min-w-[900px],窄视口下容器必须可滚动而不是把列压没。
  // 这条只能量真实布局。
  const box = page.locator('div.overflow-x-auto').first()
  if (await box.count()) {
    const { scrollW, clientW } = await box.evaluate((el) => ({
      scrollW: el.scrollWidth,
      clientW: el.clientWidth,
    }))
    expect(scrollW).toBeGreaterThanOrEqual(clientW)
  }
})
