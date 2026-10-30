import createClient from 'openapi-fetch';
import type { paths } from './api.d';

export const api = createClient<paths>({ baseUrl: '/' });
