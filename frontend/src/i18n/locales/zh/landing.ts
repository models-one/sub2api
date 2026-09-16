export default {
  batchImageGuide: {
    title: '图片批量生成',
    description: '一次提交多条提示词，任务完成后可统一下载图片结果'
  },

  // Home Page
  home: {
    viewOnGithub: '在 GitHub 上查看',
    viewDocs: '查看文档',
    docs: '文档',
    switchToLight: '切换到浅色模式',
    switchToDark: '切换到深色模式',
    dashboard: '控制台',
    login: '登录',
    getStarted: '立即开始',
    goToDashboard: '进入控制台',

    // 新增：面向用户的价值主张
    heroSubtitle: '一个密钥，畅用多个 AI 模型',

    heroDescription: '无需管理多个订阅账号，一站式接入 Claude、GPT、Gemini 等主流 AI 服务',

    tags: {
      subscriptionToApi: '订阅转 API',
      stickySession: '会话保持',
      realtimeBilling: '按量计费'
    },

    // 用户痛点区块
    painPoints: {
      title: '你是否也遇到这些问题？',
      items: {
        expensive: {
          title: '订阅费用高',
          desc: '每个 AI 服务都要单独订阅，每月支出越来越多'
        },
        complex: {
          title: '多账号难管理',
          desc: '不同平台的账号、密钥分散各处，管理起来很麻烦'
        },
        unstable: {
          title: '服务不稳定',
          desc: '单一账号容易触发限制，影响正常使用'
        },
        noControl: {
          title: '用量无法控制',
          desc: '不知道钱花在哪了，也无法限制团队成员的使用'
        }
      }
    },

    // 解决方案区块
    solutions: {
      title: '我们帮你解决',
      subtitle: '简单三步，开始省心使用 AI'
    },

    features: {
      unifiedGateway: '一键接入',
      unifiedGatewayDesc: '获取一个 API 密钥，即可调用所有已接入的 AI 模型，无需分别申请。',
      multiAccount: '稳定可靠',
      multiAccountDesc: '智能调度多个上游账号，自动切换和负载均衡，告别频繁报错。',
      balanceQuota: '用多少付多少',
      balanceQuotaDesc: '按实际使用量计费，支持设置配额上限，团队用量一目了然。'
    },

    // 优势对比
    comparison: {
      title: '为什么选择我们？',

      headers: {
        feature: '对比项',
        official: '官方订阅',
        us: '本平台'
      },

      items: {
        pricing: {
          feature: '付费方式',
          official: '固定月费，用不完也付',
          us: '按量付费，用多少付多少'
        },
        models: {
          feature: '模型选择',
          official: '单一服务商',
          us: '多模型随意切换'
        },
        management: {
          feature: '账号管理',
          official: '每个服务单独管理',
          us: '统一密钥，一站管理'
        },
        stability: {
          feature: '服务稳定性',
          official: '单账号易触发限制',
          us: '多账号池，自动切换'
        },
        control: {
          feature: '用量控制',
          official: '无法限制',
          us: '可设配额、查明细'
        }
      },

      eyebrow: 'WHY US',
      titleLine1: '两种选择，',
      titleLine2: '差距一目了然。',

      official: {
        tag: '官方订阅',
        headline: '逐家维护，逐月续费'
      },

      us: {
        tag: '本平台 · 推荐',
        headline: '一个 Key，所有可能'
      }
    },

    providers: {
      title: '已支持的 AI 模型',
      description: '一个 API，多种选择',
      supported: '已支持',
      soon: '即将推出',
      claude: 'Claude',
      gemini: 'Gemini',
      antigravity: 'Antigravity',
      more: '更多'
    },

    // CTA 区块
    cta: {
      title: '准备好开始了吗？',
      description: '注册即可获得免费试用额度，体验一站式 AI 服务',
      button: '免费注册'
    },

    footer: {
      allRightsReserved: '保留所有权利。'
    },

    modelCatalog: {
      navLabel: '模型广场'
    },

    contact: {
      eyebrow: 'CONTACT US',
      title: '需要帮助？随时联系我们',
      subtitle: '工单、技术支持、合作咨询，挑你方便的渠道。'
    },

    numbers: {
      providers: 'AI 模型供应商',
      uptime: '服务可用性',
      integrationTime: '接入耗时'
    },

    unifiedGateway: {
      eyebrow: 'UNIFIED GATEWAY',
      titleLine1: '一个密钥，',
      titleLine2: '畅用所有主流大模型。',
      description: '无需分别申请、维护多份订阅。{siteName} 把 Claude、GPT、Gemini 等服务整合到一套兼容标准的 API 之下，让接入变成几行代码的事。'
    },

    intelligentRouting: {
      eyebrow: 'INTELLIGENT ROUTING',
      titleLine1: '稳定可靠的',
      titleLine2: '智能调度引擎。',
      description: '多账号池自动负载与故障切换、会话级粘性路由、按 Token 实时计费 —— 一切只为让你的每一次请求都能在最优路径上完成。',

      bento: {
        session: {
          title: '会话保持',
          desc: '同一会话固定路由至同一账号，保留上下文记忆与多轮对话状态。'
        },

        pool: {
          title: '账号池调度',
          desc: '智能识别配额、限速、健康度，自动剔除异常账号。'
        },

        billing: {
          title: '实时计费',
          desc: '按 Token 精确计量，分钟级账单更新，支持配额上限。'
        },

        observability: {
          title: '开箱可观测',
          desc: '请求级日志、模型用量大盘、异常告警全部内置。'
        }
      }
    }
  },

  // Key Usage Query Page
  keyUsage: {
    title: 'API Key 用量查询',
    subtitle: '输入您的 API Key 以查看实时消费金额与使用状态',
    placeholder: 'sk-ant-mirror-xxxxxxxxxxxx',
    query: '查询',
    querying: '查询中...',
    privacyNote: '您的 Key 仅在浏览器本地处理，不会被存储',
    dateRange: '统计范围:',
    dateRangeToday: '今日',
    dateRange7d: '7 天',
    dateRange30d: '30 天',
    dateRange90d: '90 天',
    dateRangeCustom: '自定义',
    apply: '应用',
    used: '已使用',
    detailInfo: '详细信息',
    tokenStats: 'Token 统计',
    dailyDetail: '按日明细',
    modelStats: '模型用量统计',
    // Table headers
    date: '日期',
    model: '模型',
    requests: '请求数',
    inputTokens: '输入 Tokens',
    outputTokens: '输出 Tokens',
    cacheCreationTokens: '缓存创建',
    cacheReadTokens: '缓存读取',
    cacheWriteTokens: '缓存写入',
    totalTokens: '总 Tokens',
    cost: '费用',
    // Status
    quotaMode: 'Key 限额模式',
    walletBalance: '钱包余额',
    // Ring card titles
    totalQuota: '总额度',
    limit5h: '5 小时限额',
    limitDaily: '日限额',
    limit7d: '7 天限额',
    limitWeekly: '周限额',
    limitMonthly: '月限额',
    // Detail rows
    remainingQuota: '剩余额度',
    expiresAt: '过期时间',
    todayExpires: '(今日到期)',
    daysLeft: '({days} 天)',
    usedQuota: '已用额度',
    resetNow: '即将重置',
    subscriptionType: '订阅类型',
    billingType: '计费方式',
    subscriptionExpires: '订阅到期',
    // Usage stat cells
    todayRequests: '今日请求',
    todayInputTokens: '今日输入',
    todayOutputTokens: '今日输出',
    todayTokens: '今日 Tokens',
    todayCacheCreation: '今日缓存创建',
    todayCacheRead: '今日缓存读取',
    todayCost: '今日费用',
    rpmTpm: 'RPM / TPM',
    totalRequests: '累计请求',
    totalInputTokens: '累计输入',
    totalOutputTokens: '累计输出',
    totalTokensLabel: '累计 Tokens',
    totalCacheCreation: '累计缓存创建',
    totalCacheRead: '累计缓存读取',
    totalCost: '累计费用',
    avgDuration: '平均耗时',
    // Messages
    enterApiKey: '请输入 API Key',
    querySuccess: '查询成功',
    queryFailed: '查询失败',
    queryFailedRetry: '查询失败，请稍后重试',
    noDailyUsage: '暂无按日用量数据',
  },

  // Setup Wizard
  // Common
  setup: {
    title: '{siteName} 安装向导',
    description: '配置您的 {siteName} 实例',
    database: {
      title: '数据库配置',
      description: '连接到您的 PostgreSQL 数据库',
      host: '主机',
      port: '端口',
      username: '用户名',
      password: '密码',
      databaseName: '数据库名称',
      sslMode: 'SSL 模式',
      passwordPlaceholder: '密码',
      ssl: {
        disable: '禁用',
        require: '要求',
        verifyCa: '验证 CA',
        verifyFull: '完全验证'
      }
    },
    redis: {
      title: 'Redis 配置',
      description: '连接到您的 Redis 服务器',
      host: '主机',
      port: '端口',
      username: '用户名（可选）',
      password: '密码（可选）',
      database: '数据库',
      usernamePlaceholder: '默认用户留空',
      passwordPlaceholder: '密码',
      enableTls: '启用 TLS',
      enableTlsHint: '连接 Redis 时使用 TLS（公共 CA 证书）'
    },
    admin: {
      title: '管理员账户',
      description: '创建您的管理员账户',
      email: '邮箱',
      password: '密码',
      confirmPassword: '确认密码',
      passwordPlaceholder: '至少 8 个字符',
      confirmPasswordPlaceholder: '确认密码',
      passwordMismatch: '密码不匹配'
    },
    ready: {
      title: '准备安装',
      description: '检查您的配置并完成安装',
      database: '数据库',
      redis: 'Redis',
      adminEmail: '管理员邮箱'
    },
    status: {
      testing: '测试中...',
      success: '连接成功',
      testConnection: '测试连接',
      installing: '安装中...',
      completeInstallation: '完成安装',
      completed: '安装完成！',
      redirecting: '正在跳转到登录页面...',
      restarting: '服务正在重启，请稍候...',
      timeout: '服务重启时间超出预期，请手动刷新页面。'
    }
  },

  publicModels: {
    pageTitle: '模型广场',
    badge: '公开模型',
    title: '模型广场',
    subtitle: '所有公开分组及其支持的模型，无需登录即可浏览。',
    filterAll: '全部',
    modelCount: '共 {count} 个模型',
    empty: '当前暂无公开分组。',
    loadErrorTitle: '加载失败',
    loadErrorDescription: '请稍后刷新页面再试。',
    footnote: '专属/订阅型分组不在此页展示；最终可用模型、价格与权限以控制台为准。',
    statGroups: '分组',
    statModels: '模型',
    statPlatforms: '平台',
    searchPlaceholder: '搜索分组、平台或模型名称…',
    searchEmpty: '没有匹配的分组或模型。',
    refresh: '刷新',
    copyModelHint: '点击复制模型名称',
    apiBaseTitle: 'API 接入地址',
    apiBaseHint: '在客户端 Base URL 填入此地址，鉴权使用控制台创建的密钥。',
    copyApiBase: '复制',
    viewPricing: '查看价格',
    hidePricing: '收起价格',
    fromPrice: '输入低至 {price}',
    fromPriceRequest: '按次低至 {price}',
    fromPriceImage: '按图低至 {price}',
    noPricing: '暂未公布价格',
    groupSearchEmpty: '该分组内没有匹配的模型。',
    searchFilteredHint: '已按搜索过滤：{count} / {total} 个模型',
    rateFilterLabel: '倍率',
    priceNote:
      '本站价 = 基础价 × 分组倍率，并按当前汇率折算；基础价优先取渠道为该模型配置的专属价格，未配置时取官方价。最终计费以控制台为准。',

    table: {
      input: '输入',
      output: '输出',
      cache: '缓存',
      cacheWrite: '写入',
      cacheRead: '读取',
      unitPerMillion: '$ / 1M token',
      perUnitRequest: '/ 次',
      perUnitImage: '/ 张',
      tier: '档位'
    }
  },

  apiDocs: {
    pageTitle: '使用文档',
    empty: '暂无文档内容。',
    tocLabel: '目录',
    tocEmpty: '当前文档暂无小节。',

    entries: {
      quickstart: {
        navLabel: '快速接入'
      },

      apiGuide: {
        navLabel: '使用文档'
      }
    }
  }
};
