import { describe, expect, it, vi } from 'vitest';
import { api, ApiError } from './client';

describe('api client', () => {
  it('returns parsed health on success', async () => {
    const body = { status: 'ok', version: '0.1.0', milestone: 'M0' };
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify(body), { status: 200 })),
    );

    await expect(api.health()).resolves.toEqual(body);
  });

  it('throws ApiError carrying the status on failure', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('nope', { status: 503 })),
    );

    await expect(api.health()).rejects.toThrow(ApiError);
    await expect(api.health()).rejects.toMatchObject({ status: 503 });
  });
});
