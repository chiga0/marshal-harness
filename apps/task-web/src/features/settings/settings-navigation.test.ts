import {describe, expect, it} from 'vitest';
import {settingsReturnTo} from './settings-navigation';

describe('设置返回路径闭集', () => {
  it.each(['/', '/?status=running', '/tasks/new', '/tasks/task-1', '/tasks/task-1/team?worker=w-1', '/tasks/task-1/artifacts', '/tasks/task-1/activity'])('保留内部路径 %s', returnTo => {
    expect(settingsReturnTo({returnTo})).toBe(returnTo);
  });
  it.each(['https://evil.example', '//evil.example', '/\\evil.example', '/settings/about', '/tasks/../settings', '/tasks/%2e%2e', '/tasks/%2f%2fevil', '/tasks/%5cevil', '/tasks/a/unknown', '/tasks/a#https://evil.example', '/tasks/a\n', '/tasks/%zz', 42, null])('拒绝任意返回地址 %s', returnTo => {
    expect(settingsReturnTo({returnTo})).toBe('/');
  });
  it('无历史默认返回列表', () => expect(settingsReturnTo(undefined)).toBe('/'));
});
