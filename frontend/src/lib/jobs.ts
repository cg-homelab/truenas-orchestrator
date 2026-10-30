import { api } from './api';
import type { components } from './api.d';

export type JobDone = components['schemas']['JobDoneEvent'];
type JobLog = components['schemas']['JobLogEvent'];

const lost = 'Lost connection to the job stream.';

/**
 * Follow a job's event stream: onLine gets each log line (the log so far is replayed first),
 * onDone the final state exactly once. On a reconnect the server replays the log from the
 * start, so onReset is called first and the caller drops the lines it collected. When the
 * stream closes without a done event, the job's state is fetched instead. Returns a function
 * that stops following without calling onDone.
 */
export function followJob(
	id: string,
	onLine: (line: string) => void,
	onDone: (done: JobDone) => void,
	onReset: () => void
): () => void {
	const es = new EventSource(`/api/jobs/${encodeURIComponent(id)}/events`);
	let finished = false;
	let opened = false;
	const finish = (done: JobDone) => {
		if (finished) return;
		finished = true;
		es.close();
		onDone(done);
	};
	es.addEventListener('open', () => {
		if (opened) onReset();
		opened = true;
	});
	es.addEventListener('log', (ev) => {
		try {
			onLine((JSON.parse((ev as MessageEvent).data) as JobLog).line);
		} catch {
			// ignore malformed event
		}
	});
	es.addEventListener('done', (ev) => {
		let done: JobDone = { state: 'failed', error: 'The job ended with an unreadable result.' };
		try {
			done = JSON.parse((ev as MessageEvent).data) as JobDone;
		} catch {
			// keep the failure above
		}
		finish(done);
	});
	es.onerror = async () => {
		if (es.readyState !== EventSource.CLOSED || finished) return;
		// The stream is gone for good; ask for the job's state rather than guess.
		let done: JobDone = { state: 'failed', error: lost };
		try {
			const res = await api.GET('/api/jobs/{id}', { params: { path: { id } } });
			if (res.data?.state === 'succeeded') done = { state: 'succeeded', error: null };
			else if (res.data?.state === 'failed') done = { state: 'failed', error: res.data.error || lost };
		} catch {
			// keep the lost-connection failure
		}
		finish(done);
	};
	return () => {
		finished = true;
		es.close();
	};
}
