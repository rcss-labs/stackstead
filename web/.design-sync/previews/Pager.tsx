import { Pager } from 'stackstead-ui';

export function MiddlePage() {
  return <Pager page={2} pages={5} from={26} to={50} total={112} label="Images" onPage={() => {}} />;
}

export function FirstPage() {
  return <Pager page={1} pages={3} from={1} to={25} total={61} label="Containers" onPage={() => {}} />;
}

export function LastPage() {
  return <Pager page={3} pages={3} from={51} to={61} total={61} label="Containers" onPage={() => {}} />;
}
