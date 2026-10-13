import { emailBrand } from './brand.ts'
import * as React from 'react'
import { Body, Button, Column, Container, Head, Heading, Hr, Html, Img, Link, Preview, Row, Section, Text } from 'react-email'

export const verificationCopy = {
  'admin-login': {
    subject: '__RECHARGE_BRAND_NAME__ · 管理后台登录验证码',
    label: '管理后台',
    title: '验证管理员登录',
    description: '请在管理后台输入下方邮箱验证码，并填写管理员用户名和验证器中的 2FA 验证码。三项验证通过后才能登录。',
    link: emailBrand.admin,
    linkLabel: '返回管理后台',
  },
  'partner-welcome': {
    subject: '__RECHARGE_BRAND_NAME__ · 欢迎加入，你的代理工作台已准备好',
    label: '代理工作台',
    title: '你的代理账号已开通',
    description: '欢迎加入 __RECHARGE_BRAND_NAME__。你的专属推广链接已生成，工作台也已准备就绪。登录后，即可设置商品售价、分享推广链接，管理订单与收益。',
    link: emailBrand.workspace,
    linkLabel: '登录代理工作台',
  },
  'partner-login': {
    subject: '__RECHARGE_BRAND_NAME__ · 代理登录验证码',
    label: '代理工作台',
    title: '登录你的工作台',
    description: '请在登录页面输入下方验证码，继续管理商品、订单与收益。',
    link: emailBrand.workspace,
    linkLabel: '返回代理工作台',
  },
  'order-recovery': {
    subject: '__RECHARGE_BRAND_NAME__ · 订单找回验证码',
    label: '订单服务',
    title: '找回你的订单',
    description: '请在订单找回页面输入下方验证码，查看此邮箱关联的订单与服务进度。',
    link: emailBrand.orders,
    linkLabel: '返回我的订单',
  },
} as const

export type VerificationKind = keyof typeof verificationCopy
const font = '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", Arial, sans-serif'

export default function VerificationEmail({ kind = 'partner-login', code = '482619' }: { kind?: VerificationKind; code?: string }) {
  const copy = verificationCopy[kind]
  const invitation = kind === 'partner-welcome'
  return <Html lang="zh-CN">
    <Head>
      <meta name="color-scheme" content="light dark" />
      <meta name="supported-color-schemes" content="light dark" />
      <style>{`
        @media only screen and (max-width: 600px) {
          .email-body > table > tbody > tr > td { padding: 24px 12px !important; }
          .email-content > tbody > tr > td { padding: 28px 24px !important; }
          .email-title { font-size: 26px !important; }
          .email-code { font-size: 34px !important; letter-spacing: 6px !important; }
        }
        @media (prefers-color-scheme: dark) {
          .email-body, .email-body > table > tbody > tr > td { background-color: #101011 !important; }
          .email-card { background-color: #1d1d1f !important; border-color: #353538 !important; }
          .email-ink { color: #f5f5f7 !important; }
          .email-muted { color: #b5b5bc !important; }
          .email-divider { border-color: #353538 !important; }
          .email-code-panel { background-color: #29292c !important; }
          .email-link { color: #70b5ff !important; }
          .email-button { background-color: #0071e3 !important; color: #ffffff !important; }
        }
      `}</style>
    </Head>
    <Preview>{invitation ? '你的代理账号已开通。打开工作台，使用收件邮箱获取验证码并登录。' : `${copy.title} · 验证码 10 分钟内有效。请勿与他人分享。`}</Preview>
    <Body lang="zh-CN" className="email-body" style={{ margin: 0, padding: '48px 16px', backgroundColor: '#f5f5f7', fontFamily: font }}>
      <Container style={{ maxWidth: '520px', margin: '0 auto' }}>
        <Section style={{ padding: '0 8px 24px' }}>
          <Row><Column style={{ width: '42px' }}><Img src={emailBrand.logo} width="32" height="32" alt="" style={{ display: 'block', borderRadius: '9px' }} /></Column>
            <Column><Text className="email-ink" style={{ wordBreak: 'break-all', margin: 0, color: '#1d1d1f', fontSize: '21px', fontWeight: 650, letterSpacing: '-0.6px' }}>{emailBrand.name}</Text></Column>
            <Column align="right"><Text className="email-muted" style={{ margin: 0, color: '#6e6e73', fontSize: '12px' }}>{copy.label}</Text></Column></Row>
        </Section>
        <Section className="email-card" style={{ backgroundColor: '#ffffff', border: '1px solid #e5e5ea', borderRadius: '20px' }}>
          <Section className="email-content" style={{ padding: '36px 36px 32px' }}>
            <Text className="email-muted" style={{ margin: '0 0 12px', fontSize: '11px', color: '#6e6e73', letterSpacing: '2px' }}>{invitation ? '欢迎成为合作伙伴' : '身份验证'}</Text>
            <Heading className="email-title email-ink" as="h1" style={{ margin: '0 0 16px', fontSize: '30px', lineHeight: '1.3', fontWeight: 650, letterSpacing: '-0.8px', color: '#1d1d1f' }}>{copy.title}</Heading>
            <Text className="email-muted" style={{ wordBreak: 'break-word', margin: '0 0 28px', fontSize: '14px', lineHeight: '24px', color: '#6e6e73' }}>{copy.description}</Text>
            {invitation ? <>
              <Button className="email-button" href={copy.link} style={{ backgroundColor: '#0071e3', color: '#ffffff', borderRadius: '10px', padding: '15px 24px', fontSize: '15px', fontWeight: 600, lineHeight: '20px', textDecoration: 'none', textAlign: 'center', boxSizing: 'border-box' }}>进入代理工作台 →</Button>
              <Section className="email-code-panel" style={{ backgroundColor: '#f5f5f7', borderRadius: '12px', padding: '22px 24px', marginTop: '28px' }}>
                <Text className="email-ink" style={{ margin: '0 0 14px', fontSize: '14px', fontWeight: 600, color: '#1d1d1f' }}>三步开始使用</Text>
                {['打开代理工作台，填写接收本邮件的邮箱。', '点击「获取验证码」，查收登录验证码邮件。', '输入验证码完成登录，查看专属推广链接。'].map((step, index) => <Row key={step} style={{ marginTop: index ? '12px' : 0 }}><Column valign="top" style={{ width: '26px' }}><Text style={{ margin: 0, fontSize: '13px', lineHeight: '22px', color: '#0071e3', fontWeight: 600 }}>0{index + 1}</Text></Column><Column><Text className="email-muted" style={{ margin: 0, fontSize: '13px', lineHeight: '22px', color: '#6e6e73' }}>{step}</Text></Column></Row>)}
              </Section>
              <Text className="email-muted" style={{ margin: '20px 0 0', fontSize: '12px', lineHeight: '21px', color: '#6e6e73' }}>无需设置密码。准备登录时再获取验证码即可，登录入口可收藏以便下次使用。</Text>
            </> : <><Section className="email-code-panel" style={{ backgroundColor: '#f5f5f7', borderRadius: '12px', padding: '23px 8px 20px', textAlign: 'center' }}>
              <Text className="email-code email-ink" style={{ margin: '0 0 10px', fontFamily: '"SFMono-Regular", Consolas, "Liberation Mono", monospace', fontSize: '40px', fontWeight: 600, lineHeight: '48px', letterSpacing: '8px', color: '#1d1d1f' }}>{code}</Text>
              <Text className="email-muted" style={{ margin: 0, fontSize: '12px', color: '#6e6e73' }}>验证码 · 10 分钟内有效</Text>
            </Section>
            <Text className="email-muted" style={{ margin: '22px 0 0', fontSize: '12px', lineHeight: '21px', color: '#6e6e73' }}>请勿向任何人提供验证码，包括声称是客服的人。如果不是你本人操作，忽略此邮件即可。</Text></>}
            <Hr className="email-divider" style={{ border: 'none', borderTop: '1px solid #e5e5ea', margin: '26px 0 22px' }} />
            {invitation ? <><Text className="email-muted" style={{ margin: '0 0 4px', fontSize: '12px', lineHeight: '21px', color: '#6e6e73' }}>按钮无法打开？复制下方链接到浏览器：</Text><Link className="email-link" href={copy.link} style={{ color: '#0071e3', fontSize: '13px', textDecoration: 'none', lineHeight: '24px', wordBreak: 'break-all' }}>{copy.link}</Link></> : <Link className="email-link" href={copy.link} style={{ color: '#0071e3', fontSize: '13px', fontWeight: 500, textDecoration: 'none', display: 'inline-block', lineHeight: '28px' }}>{copy.linkLabel} →</Link>}
          </Section>
        </Section>
        <Section style={{ padding: '22px 8px 0' }}>
          <Text className="email-muted" style={{ wordBreak: 'break-all', margin: '0 0 5px', color: '#86868b', fontSize: '11px', lineHeight: '19px' }}>{emailBrand.name} · 让工具回归简单</Text>
          <Text className="email-muted" style={{ margin: 0, color: '#86868b', fontSize: '11px', lineHeight: '19px' }}>此邮件由系统自动发送，请勿直接回复。<br />独立服务商，非品牌官方网站。</Text>
        </Section>
      </Container>
    </Body>
  </Html>
}
