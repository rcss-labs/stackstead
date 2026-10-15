<script lang="ts">
  let { label = 'Loading' }: { label?: string } = $props();
</script>

<span role="status" class="spinner"><span class="ring" aria-hidden="true"></span><span class="visually-hidden">{label}</span></span>

<style>
  .spinner { display: inline-flex; }
  .ring {
    width: 16px;
    height: 16px;
    border: 2px solid var(--border);
    border-top-color: var(--accent);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
