import { test } from 'node:test'
import assert from 'node:assert/strict'
import { emailProblem, emailSuggestions, orderEmailProblem } from '../src/lib/email.ts'

test('new purchases require exact common email domains while recovery format validation stays compatible', () => {
  for (const value of [' User+orders@GMAIL.COM ', 'a@qq.com', 'a@163.com', 'a@outlook.com', 'a@icloud.com', 'a@proton.me']) assert.equal(orderEmailProblem(value), '')
  for (const value of ['a@example.com', 'a@example.invalid', 'a@company.com', 'a@evilgmail.com', 'a@gmail.com.evil.test', 'a@sub.gmail.com', 'a@gmail.com.', 'a@@qq.com']) assert.notEqual(orderEmailProblem(value), '')
  assert.equal(emailProblem('legacy@company.com'), '')
})

test('checkout accepts trimmed addresses and rejects incomplete or oversized contact addresses', () => {
  for (const value of ['person@example.com', '  User+orders@sub.example.co.uk  ', '用户@example.com']) assert.equal(emailProblem(value), '')
  for (const value of ['', '   ', 'person', 'person@', '@example.com', 'person@example', 'a b@example.com', 'a@@example.com', `${'a'.repeat(250)}@example.com`]) assert.notEqual(emailProblem(value), '')
})

test('checkout suggests common domains and filters a partially entered suffix without changing the local part', () => {
  const expected = ['gmail.com', 'qq.com', 'outlook.com', '163.com', 'icloud.com'].map(domain => `User+orders@${domain}`)
  assert.deepEqual(emailSuggestions('User+orders'), expected)
  assert.deepEqual(emailSuggestions(' User+orders@ '), expected)
  assert.deepEqual(emailSuggestions('User+orders@Gm'), ['User+orders@gmail.com'])
  assert.deepEqual(emailSuggestions('test@1'), ['test@163.com'])
})

test('checkout leaves complete/custom addresses alone and never suggests malformed or oversized addresses', () => {
  for (const value of ['', ' ', '@', '@gmail.com', 'a@@g', 'a b@g', 'test@gmail.com', 'test@GMAIL.COM', 'test@company.example', `${'a'.repeat(250)}@`]) {
    assert.deepEqual(emailSuggestions(value), [], value)
  }
  for (const suggestion of emailSuggestions('a'.repeat(247))) {
    assert.equal(emailProblem(suggestion), '')
  }
})
