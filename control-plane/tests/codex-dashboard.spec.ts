import { expect, test } from '@playwright/test';
import fs from 'node:fs/promises';
import path from 'node:path';

for (const locale of ['en', 'ru'] as const) {
  test(`Codex accounts render safely in ${locale}`, async ({ page }, testInfo) => {
    const consoleErrors: string[] = [];
    page.on('console', (message) => {
      if (message.type() === 'error') consoleErrors.push(message.text());
    });

    await page.goto('/');
    await page.getByRole('button', { name: locale.toUpperCase(), exact: true }).click();
    await page.getByRole('button', { name: locale === 'en' ? /Accounts/ : /Аккаунты/ }).click();

    await expect(page.locator('html')).toHaveAttribute('lang', locale);
    await expect(page.getByRole('heading', { name: locale === 'en' ? 'ChatGPT Codex accounts' : 'Аккаунты ChatGPT Codex' })).toBeVisible();
    await expect(page.getByText(locale === 'en' ? 'Authentication' : 'Авторизация').first()).toBeVisible();
    await expect(page.getByText(locale === 'en' ? /Reset credits: \d+/ : /Кредитов сброса: \d+/).first()).toBeVisible();

    expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);

    await page.getByRole('button', { name: locale === 'en' ? 'Codex account' : 'Codex-аккаунт' }).click();
    await expect(page.getByRole('dialog')).toBeVisible();
    for (const tab of locale === 'en'
      ? ['Browser sign-in', 'Device Code', 'Import auth.json']
      : ['Вход в браузере', 'Код устройства', 'Импорт auth.json']) {
      await expect(page.getByRole('tab', { name: tab, exact: true })).toBeVisible();
    }
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);

    const target = path.resolve('../artifacts/playwright/codex', testInfo.project.name, locale);
    await fs.mkdir(target, { recursive: true });
    await page.screenshot({ path: path.join(target, 'accounts.png'), fullPage: true });
    expect(consoleErrors).toEqual([]);
  });
}

test('Account management shows deletion and inherits the provider endpoint', async ({ page }, testInfo) => {
  await page.goto('/');
  await page.getByRole('button', { name: 'EN', exact: true }).click();
  await page.getByRole('button', { name: /Accounts/ }).click();

  const providerRow = page.locator('.provider-table').getByRole('row').filter({ has: page.getByText('test', { exact: true }) });
  await expect(providerRow).toBeVisible();
  await expect(providerRow.getByRole('button', { name: 'Delete' })).toBeVisible();

  await page.getByRole('button', { name: 'Account', exact: true }).click();
  const accountDialog = page.getByRole('dialog');
  await accountDialog.getByLabel('Provider').selectOption({ label: 'test' });
  await expect(accountDialog.getByLabel('Base URL')).toHaveValue('https://tokenharbor.ai/v1');
  await expect(accountDialog.getByLabel('Base URL')).toHaveAttribute('readonly', '');
  await expect(accountDialog.getByText('Weight', { exact: true })).toHaveCount(0);
  await expect(accountDialog.getByText('Cost', { exact: true })).toHaveCount(0);

  const target = path.resolve('../artifacts/playwright/accounts', testInfo.project.name);
  await fs.mkdir(target, { recursive: true });
  await page.screenshot({ path: path.join(target, 'provider-endpoint.png'), fullPage: true });
  await page.keyboard.press('Escape');

  await providerRow.getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByRole('heading', { name: 'Delete provider?' })).toBeVisible();
  await page.screenshot({ path: path.join(target, 'delete-provider.png'), fullPage: true });
  await page.getByRole('button', { name: 'Cancel' }).click();
});
