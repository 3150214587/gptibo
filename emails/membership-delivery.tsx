import { emailBrand } from './brand.ts'
import * as React from 'react'
import { Body, Button, Container, Head, Heading, Html, Img, Preview, Section, Text } from 'react-email'

export default function MembershipDeliveryEmail({ kind = 'link', product }: { kind?: 'link' | 'cdk'; product?: 'gemini' | 'duolingo' }) {
  const link = kind === 'link'
  const duo = product === 'duolingo' || (!product && !link)
  const name = duo ? '多邻国 1 年会员' : 'Gemini Pro 18 个月会员'
  return <Html lang="zh-CN"><Head><meta name="color-scheme" content="light" /><style>{'@media only screen and (max-width:600px){.delivery-content{padding:28px 22px!important}}'}</style></Head>
    <Preview>您的{name}{link ? '领取链接' : '兑换 CDK'}已准备好。</Preview>
    <Body style={{ margin: 0, padding: '40px 16px', background: '#f5f5f7', color: '#202124', fontFamily: '-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC",Arial,sans-serif' }}>
      <Container style={{ maxWidth: '560px', margin: '0 auto' }}>
        <Img src={emailBrand.logo} width="32" height="32" alt={emailBrand.name} style={{ marginBottom: '24px' }} />
        <Section className="delivery-content" style={{ padding: '36px', background: '#fff', borderRadius: '20px', borderTop: `4px solid ${duo ? '#58b500' : '#7872ef'}` }}>
          <Text style={{ wordBreak: 'break-all', fontSize: '12px', letterSpacing: '2px', color: '#72757c', margin: '0 0 16px' }}>{emailBrand.name} · 邮件交付</Text>
          <Heading as="h1" style={{ fontSize: '28px', lineHeight: '1.35', margin: '0 0 12px' }}>{duo ? '新一年的学习，从这里开始。' : '您的会员，待领取。'}</Heading>
          <Text style={{ fontSize: '16px', fontWeight: 600 }}>{duo ? '多邻国 · 1 年会员' : 'Gemini Pro · 18 个月会员'}</Text>
          <Text style={{ fontSize: '14px', lineHeight: '24px', color: '#62656d' }}>我们已为您的订单准备好{link ? '领取链接。请使用您要领取会员的账号打开链接，核对领取页面的资格与说明。' : '兑换 CDK。请按照下方说明，在对应兑换页面输入兑换码。'}</Text>
          {link && <>
            <Section style={{ textAlign: 'center', margin: '24px 0 12px' }}>
              <Button href="__RECHARGE_DELIVERY_URL__" style={{ background: duo ? '#3f7d12' : '#6256cf', color: '#fff', borderRadius: '10px', padding: '14px 28px', fontSize: '15px', fontWeight: 600, textDecoration: 'none' }}>立即领取</Button>
            </Section>
            <Text style={{ textAlign: 'center', fontSize: '12px', lineHeight: '20px', color: '#62656d' }}>点击按钮直接前往领取，也可以复制下方链接到浏览器打开。</Text>
          </>}
          <Section style={{ background: duo ? '#f2f9e9' : '#f3f1fd', padding: '22px', borderRadius: '12px', margin: '24px 0' }}>
            <Text style={{ margin: '0 0 10px', fontSize: '12px', color: '#62656d' }}>{link ? '专属领取链接' : '您的兑换 CDK'}</Text>
            <Text style={{ margin: 0, fontSize: link ? '14px' : '22px', fontWeight: 600, lineHeight: '28px', wordBreak: 'break-all', fontFamily: link ? 'inherit' : 'monospace' }}>__RECHARGE_DELIVERY_VALUE__</Text>
          </Section>
          <Text style={{ fontSize: '14px', fontWeight: 600, marginBottom: '6px' }}>领取说明</Text>
          <Text style={{ fontSize: '14px', lineHeight: '24px', color: '#62656d', whiteSpace: 'pre-line' }}>__RECHARGE_DELIVERY_INSTRUCTIONS__</Text>
          <Text style={{ fontSize: '12px', lineHeight: '22px', color: '#858790', wordBreak: 'break-all', borderTop: '1px solid #ececf0', paddingTop: '20px', marginTop: '28px' }}>订单号：__RECHARGE_DELIVERY_ORDER__<br />请妥善保管{link ? '领取链接' : '兑换码'}，不要转发给他人。邮件发出表示商品已发货，实际激活结果请在会员平台核对。</Text>
          <Text style={{ fontSize: '13px', lineHeight: '22px', color: '#62656d' }}>如需帮助，请回到 <a href={emailBrand.orders} style={{ color: '#0071e3' }}>我的订单</a>，通过原订单联系支持或申请售后。</Text>
        </Section>
        <Text style={{ wordBreak: 'break-all', fontSize: '11px', lineHeight: '20px', color: '#858790', padding: '8px' }}>{emailBrand.name} · 独立服务商<br />此邮件仅用于您购买的商品交付，请勿直接回复。</Text>
      </Container>
    </Body>
  </Html>
}
