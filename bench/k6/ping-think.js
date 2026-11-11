// k6 counterpart of the memory test: one GET then THINK seconds of sleep.
import http from 'k6/http';
import { sleep } from 'k6';

export const options = {
  discardResponseBodies: true,
};

const url = __ENV.URL || 'http://127.0.0.1:18080/ping';
const think = parseFloat(__ENV.THINK || '1');

export default function () {
  http.get(url);
  sleep(think);
}
