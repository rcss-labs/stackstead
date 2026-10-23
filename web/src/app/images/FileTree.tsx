import { useId, useMemo, useRef, useState } from 'react';
import type { ImageFile } from '$lib/api/imageTypes';
import { formatBytes } from '$lib/format';
import { relativeUploadPaths } from '$lib/images/upload';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Dialog } from '../components/Dialog';
import { Field } from '../components/Field';
import styles from './images.module.css';

interface TreeNode {
  name: string;
  path: string;
  file?: ImageFile;
  children: TreeNode[];
}

function buildTree(files: ImageFile[]): TreeNode[] {
  const root: TreeNode = { name: '', path: '', children: [] };
  for (const file of files) {
    const parts = file.path.split('/');
    let at = root;
    parts.forEach((part, i) => {
      const path = parts.slice(0, i + 1).join('/');
      let next = at.children.find((c) => c.name === part && (i < parts.length - 1) === (c.file === undefined));
      if (!next) {
        next = { name: part, path, children: [], file: i === parts.length - 1 ? file : undefined };
        at.children.push(next);
      }
      at = next;
    });
  }
  const order = (nodes: TreeNode[]) => {
    nodes.sort((a, b) => Number(a.file !== undefined) - Number(b.file !== undefined) || a.name.localeCompare(b.name));
    nodes.forEach((n) => order(n.children));
  };
  order(root.children);
  return root.children;
}

export function FileTree(props: {
  files: ImageFile[];
  onUpload(files: { path: string; blob: File }[]): void;
  onRename(from: string, to: string): void;
  onDelete(path: string): void;
}) {
  const { files, onUpload, onRename, onDelete } = props;
  const uid = useId();
  const tree = useMemo(() => buildTree(files), [files]);
  const filePicker = useRef<HTMLInputElement>(null);
  const folderPicker = useRef<HTMLInputElement>(null);
  const [renaming, setRenaming] = useState<string>();
  const [newPath, setNewPath] = useState('');
  const [deleting, setDeleting] = useState<string>();

  function picked(input: HTMLInputElement) {
    const list = Array.from(input.files ?? []);
    input.value = '';
    if (list.length > 0) onUpload(relativeUploadPaths(list));
  }

  function renderNodes(nodes: TreeNode[]) {
    return nodes.map((node) => (
      <li key={node.path} role="treeitem" aria-selected={false} aria-expanded={node.file ? undefined : true}>
        <div className={styles.node}>
          {node.file ? (
            <>
              <span>{node.name}</span>
              <span className={styles.size}>{formatBytes(node.file.size)}</span>
              <Button
                variant="ghost"
                size="xs"
                aria-label={`Rename ${node.path}`}
                onClick={() => {
                  setNewPath(node.path);
                  setRenaming(node.path);
                }}
              >
                Rename
              </Button>
              <Button variant="ghost" size="xs" aria-label={`Delete ${node.path}`} onClick={() => setDeleting(node.path)}>
                Delete
              </Button>
            </>
          ) : (
            <span className={styles.dir}>{node.name}</span>
          )}
        </div>
        {!node.file && node.children.length > 0 && <ul role="group">{renderNodes(node.children)}</ul>}
      </li>
    ));
  }

  return (
    <div className={styles.pad}>
      <div className={styles.row}>
        <strong>Build context</strong>
        <span className={styles.spacer} />
        <Button variant="secondary" size="sm" onClick={() => filePicker.current?.click()}>
          Upload files
        </Button>
        <Button variant="secondary" size="sm" onClick={() => folderPicker.current?.click()}>
          Upload folder
        </Button>
        <input ref={filePicker} type="file" multiple hidden aria-label="Upload files" onChange={(e) => picked(e.currentTarget)} />
        <input
          ref={folderPicker}
          type="file"
          hidden
          aria-label="Upload folder"
          {...({ webkitdirectory: '', directory: '' } as Record<string, string>)}
          onChange={(e) => picked(e.currentTarget)}
        />
      </div>
      {files.length === 0 ? (
        <p className={styles.muted}>No files yet. Upload the Dockerfile and anything it copies.</p>
      ) : (
        <ul role="tree" aria-label="Build context files" className={styles.tree}>
          {renderNodes(tree)}
        </ul>
      )}

      {renaming !== undefined && (
        <Dialog open title="Rename file" onClose={() => setRenaming(undefined)}>
          <form
            className={styles.form}
            onSubmit={(e) => {
              e.preventDefault();
              const from = renaming;
              setRenaming(undefined);
              if (newPath.trim() && newPath.trim() !== from) onRename(from, newPath.trim());
            }}
          >
            <Field label="New path" id={`${uid}-rename`} mono hint="Use a folder prefix to move the file.">
              {(control) => <input {...control} value={newPath} onChange={(e) => setNewPath(e.target.value)} />}
            </Field>
            <div className={styles.actions}>
              <Button variant="secondary" onClick={() => setRenaming(undefined)}>
                Cancel
              </Button>
              <Button variant="primary" type="submit">
                Rename
              </Button>
            </div>
          </form>
        </Dialog>
      )}
      <ConfirmDialog
        open={deleting !== undefined}
        title="Delete file"
        message={`Delete ${deleting ?? ''} from the build context?`}
        confirmLabel="Delete file"
        danger
        onConfirm={() => {
          const path = deleting;
          setDeleting(undefined);
          if (path !== undefined) onDelete(path);
        }}
        onCancel={() => setDeleting(undefined)}
      />
    </div>
  );
}
