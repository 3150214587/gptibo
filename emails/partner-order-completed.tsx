import { emailBrand } from './brand.ts'
import * as React from 'react'
import { Body, Button, Container, Head, Heading, Html, Img, Preview, Section, Text } from 'react-email'

export default function PartnerOrderCompletedEmail() {
  const rows = [
    ['商品套餐', '__RECHARGE_ORDER_PRODUCT__'],
    ['客户邮箱', '__RECHARGE_ORDER_CUSTOMER__'],
    ['完成时间', '__RECHARGE_ORDER_COMPLETED__（北京时间）'],
    ['订单编号', '__RECHARGE_ORDER_ID__'],
  ]
  return <Html lang="zh-CN"><Head><meta name="color-scheme" content="light" /><style>{'@media only screen and (max-width:600px){.order-email-content{padding:28px 22px!important}.order-email-title{font-size:25px!important}.order-email-detail{font-size:13px!important}}'}</style></Head>
    <Preview>你的商店已完成一笔订单 · __RECHARGE_ORDER_PRODUCT__ · __RECHARGE_ORDER_AMOUNT__</Preview>
    <Body style={{ margin: 0, padding: '40px 16px', background: '#f5f5f7', color: '#202124', fontFamily: '-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC",Arial,sans-serif' }}>
      <Container style={{ maxWidth: '560px', margin: '0 auto' }}>
        <Img src={emailBrand.logo} width="32" height="32" alt={emailBrand.name} style={{ marginBottom: '24px' }} />
        <Section className="order-email-content" style={{ padding: '36px', background: '#ffffff', borderRadius: '20px', borderTop: '4px solid #0071e3' }}>
          <Text style={{ wordBreak: 'break-all', fontSize: '12px', letterSpacing: '2px', color: '#72757c', margin: '0 0 16px' }}>{emailBrand.name} · 订单完成通知</Text>
          <Heading className="order-email-title" as="h1" style={{ fontSize: '28px', lineHeight: '1.4', margin: '0 0 12px' }}>你的商店，又完成一单。</Heading>
          <Text style={{ fontSize: '14px', lineHeight: '24px', color: '#62656d', margin: '0 0 24px', wordBreak: 'break-all' }}>__RECHARGE_SHOP_NAME__，以下订单已完成，详情为你整理好了。</Text>
          <Section style={{ background: '#f3f7fc', padding: '24px', borderRadius: '12px', marginBottom: '26px' }}>
            <Text style={{ fontSize: '12px', color: '#62656d', margin: '0 0 8px' }}>订单金额 <span style={{ color: '#20794a', marginLeft: '12px' }}>● 已完成</span></Text>
            <Text style={{ fontSize: '36px', fontWeight: 600, letterSpacing: '-1px', lineHeight: '44px', margin: '0' }}>__RECHARGE_ORDER_AMOUNT__</Text>
          </Section>
          {rows.map(([label, value]) => <Section key={label} style={{ borderBottom: '1px solid #ececf0', padding: '12px 0' }}>
            <Text style={{ fontSize: '11px', color: '#858790', margin: '0 0 5px' }}>{label}</Text>
            <Text className="order-email-detail" style={{ fontSize: '14px', lineHeight: '23px', fontWeight: 500, margin: 0, wordBreak: 'break-all' }}>{value}</Text>
          </Section>)}
          <Section style={{ marginTop: '28px' }}><Button href={emailBrand.workspace + '?tab=orders'} style={{ background: '#0071e3', color: '#fff', borderRadius: '10px', padding: '14px 24px', fontSize: '14px', fontWeight: 600, textDecoration: 'none' }}>查看订单收益</Button></Section>
          <Text style={{ fontSize: '12px', lineHeight: '21px', color: '#858790', margin: '18px 0 0' }}>订单金额为客户购买金额，实际可结算收益请以代理工作台为准。</Text>
        </Section>
        <Text style={{ fontSize: '11px', lineHeight: '21px', color: '#858790', padding: '8px', wordBreak: 'break-all' }}>你已开启商店订单通知，可在代理工作台的「商店设置」中关闭。<br />{emailBrand.name} · 此邮件由系统自动发送，请勿直接回复。</Text>
      </Container>
    </Body>
  </Html>
}
